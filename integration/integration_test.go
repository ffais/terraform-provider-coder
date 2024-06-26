package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration performs an integration test against an ephemeral Coder deployment.
// For each directory containing a `main.tf` under `/integration`, performs the following:
//   - Pushes the template to a temporary Coder instance running in Docker
//   - Creates a workspace from the template. Templates here are expected to create a
//     local_file resource containing JSON that can be marshalled as a map[string]string
//   - Fetches the content of the JSON file created and compares it against the expected output.
//
// NOTE: all interfaces to this Coder deployment are performed without github.com/coder/coder/v2/codersdk
// in order to avoid a circular dependency.
func TestIntegration(t *testing.T) {
	if os.Getenv("TF_ACC") == "1" {
		t.Skip("Skipping integration tests during tf acceptance tests")
	}

	timeoutStr := os.Getenv("TIMEOUT_MINS")
	if timeoutStr == "" {
		timeoutStr = "10"
	}
	timeoutMins, err := strconv.Atoi(timeoutStr)
	require.NoError(t, err, "invalid value specified for timeout")
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMins)*time.Minute)
	t.Cleanup(cancel)

	// Given: we have an existing Coder deployment running locally
	ctrID := setup(ctx, t)

	for _, tt := range []struct {
		// Name of the folder under `integration/` containing a test template
		templateName string
		// map of string to regex to be passed to assertOutput()
		expectedOutput map[string]string
	}{
		{
			templateName: "test-data-source",
			expectedOutput: map[string]string{
				"provisioner.arch":                  runtime.GOARCH,
				"provisioner.id":                    `[a-zA-Z0-9-]+`,
				"provisioner.os":                    runtime.GOOS,
				"workspace.access_port":             `\d+`,
				"workspace.access_url":              `https?://\D+:\d+`,
				"workspace.id":                      `[a-zA-z0-9-]+`,
				"workspace.name":                    `test-data-source`,
				"workspace.owner":                   `testing`,
				"workspace.owner_email":             `testing@coder\.com`,
				"workspace.owner_groups":            `\[\]`,
				"workspace.owner_id":                `[a-zA-Z0-9]+`,
				"workspace.owner_name":              `default`,
				"workspace.owner_oidc_access_token": `^$`, // TODO: need a test OIDC integration
				"workspace.owner_session_token":     `[a-zA-Z0-9-]+`,
				"workspace.start_count":             `1`,
				"workspace.template_id":             `[a-zA-Z0-9-]+`,
				"workspace.template_name":           `test-data-source`,
				"workspace.template_version":        `.+`,
				"workspace.transition":              `start`,
				"workspace_owner.email":             `testing@coder\.com`,
				"workspace_owner.full_name":         `default`,
				"workspace_owner.groups":            `\[\]`,
				"workspace_owner.id":                `[a-zA-Z0-9-]+`,
				"workspace_owner.name":              `testing`,
				"workspace_owner.oidc_access_token": `^$`, // TODO: test OIDC integration
				"workspace_owner.session_token":     `.+`,
				"workspace_owner.ssh_private_key":   `^$`, // Depends on coder/coder#13366
				"workspace_owner.ssh_public_key":    `^$`, // Depends on coder/coder#13366
			},
		},
	} {
		t.Run(tt.templateName, func(t *testing.T) {
			// Import named template
			_, rc := execContainer(ctx, t, ctrID, fmt.Sprintf(`coder templates push %s --directory /src/integration/%s --var output_path=/tmp/%s.json --yes`, tt.templateName, tt.templateName, tt.templateName))
			require.Equal(t, 0, rc)
			// Create a workspace
			_, rc = execContainer(ctx, t, ctrID, fmt.Sprintf(`coder create %s -t %s --yes`, tt.templateName, tt.templateName))
			require.Equal(t, 0, rc)
			// Fetch the output created by the template
			out, rc := execContainer(ctx, t, ctrID, fmt.Sprintf(`cat /tmp/%s.json`, tt.templateName))
			require.Equal(t, 0, rc)
			actual := make(map[string]string)
			require.NoError(t, json.NewDecoder(strings.NewReader(out)).Decode(&actual))
			assertOutput(t, tt.expectedOutput, actual)
		})
	}
}

func setup(ctx context.Context, t *testing.T) string {
	var (
		// For this test to work, we pass in a custom terraformrc to use
		// the locally built version of the provider.
		testTerraformrc = `provider_installation {
		dev_overrides {
		  "coder/coder" = "/src"
		}
		  direct{}
	  }`
		localURL = "http://localhost:3000"
	)

	coderImg := os.Getenv("CODER_IMAGE")
	if coderImg == "" {
		coderImg = "ghcr.io/coder/coder"
	}

	coderVersion := os.Getenv("CODER_VERSION")
	if coderVersion == "" {
		coderVersion = "latest"
	}

	t.Logf("using coder image %s:%s", coderImg, coderVersion)

	// Ensure the binary is built
	binPath, err := filepath.Abs("../terraform-provider-coder")
	require.NoError(t, err)
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		t.Fatalf("not found: %q - please build the provider first", binPath)
	}
	tmpDir := t.TempDir()
	// Create a terraformrc to point to our freshly built provider!
	tfrcPath := filepath.Join(tmpDir, "integration.tfrc")
	err = os.WriteFile(tfrcPath, []byte(testTerraformrc), 0o644)
	require.NoError(t, err, "write terraformrc to tempdir")

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err, "init docker client")

	srcPath, err := filepath.Abs("..")
	require.NoError(t, err, "get abs path of parent")
	t.Logf("src path is %s\n", srcPath)

	// Stand up a temporary Coder instance
	ctr, err := cli.ContainerCreate(ctx, &container.Config{
		Image: coderImg + ":" + coderVersion,
		Env: []string{
			"CODER_ACCESS_URL=" + localURL,             // Set explicitly to avoid creating try.coder.app URLs.
			"CODER_IN_MEMORY=true",                     // We don't necessarily care about real persistence here.
			"CODER_TELEMETRY_ENABLE=false",             // Avoid creating noise.
			"TF_CLI_CONFIG_FILE=/tmp/integration.tfrc", // Our custom tfrc from above.
		},
		Labels: map[string]string{},
	}, &container.HostConfig{
		Binds: []string{
			tfrcPath + ":/tmp/integration.tfrc", // Custom tfrc from above.
			srcPath + ":/src",                   // Bind-mount in the repo with the built binary and templates.
		},
	}, nil, nil, "")
	require.NoError(t, err, "create test deployment")

	t.Logf("created container %s\n", ctr.ID)
	t.Cleanup(func() { // Make sure we clean up after ourselves.
		// TODO: also have this execute if you Ctrl+C!
		t.Logf("stopping container %s\n", ctr.ID)
		_ = cli.ContainerRemove(ctx, ctr.ID, container.RemoveOptions{
			Force: true,
		})
	})

	err = cli.ContainerStart(ctx, ctr.ID, container.StartOptions{})
	require.NoError(t, err, "start container")
	t.Logf("started container %s\n", ctr.ID)

	// nolint:gosec // For testing only.
	var (
		testEmail    = "testing@coder.com"
		testPassword = "InsecurePassw0rd!"
		testUsername = "testing"
	)

	// Wait for container to come up
	require.Eventually(t, func() bool {
		_, rc := execContainer(ctx, t, ctr.ID, fmt.Sprintf(`curl -s --fail %s/api/v2/buildinfo`, localURL))
		if rc == 0 {
			return true
		}
		t.Logf("not ready yet...")
		return false
	}, 10*time.Second, time.Second, "coder failed to become ready in time")

	// Perform first time setup
	_, rc := execContainer(ctx, t, ctr.ID, fmt.Sprintf(`coder login %s --first-user-email=%q --first-user-password=%q --first-user-trial=false --first-user-username=%q`, localURL, testEmail, testPassword, testUsername))
	require.Equal(t, 0, rc, "failed to perform first-time setup")
	return ctr.ID
}

// execContainer executes the given command in the given container and returns
// the output and the exit code of the command.
func execContainer(ctx context.Context, t *testing.T, containerID, command string) (string, int) {
	t.Helper()
	t.Logf("exec container cmd: %q", command)
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err, "connect to docker")
	defer cli.Close()
	execConfig := types.ExecConfig{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"/bin/sh", "-c", command},
	}
	ex, err := cli.ContainerExecCreate(ctx, containerID, execConfig)
	require.NoError(t, err, "create container exec")
	resp, err := cli.ContainerExecAttach(ctx, ex.ID, types.ExecStartCheck{})
	require.NoError(t, err, "attach to container exec")
	defer resp.Close()
	var buf bytes.Buffer
	_, err = stdcopy.StdCopy(&buf, &buf, resp.Reader)
	require.NoError(t, err, "read stdout")
	out := buf.String()
	t.Log("exec container output:\n" + out)
	execResp, err := cli.ContainerExecInspect(ctx, ex.ID)
	require.NoError(t, err, "get exec exit code")
	return out, execResp.ExitCode
}

// assertOutput asserts that, for each key-value pair in expected:
// 1. actual[k] as a regex matches expected[k], and
// 2. the set of keys of expected are not a subset of actual.
func assertOutput(t *testing.T, expected, actual map[string]string) {
	t.Helper()

	for expectedKey, expectedValExpr := range expected {
		actualVal := actual[expectedKey]
		assert.Regexp(t, expectedValExpr, actualVal)
	}
	for actualKey := range actual {
		_, ok := expected[actualKey]
		assert.True(t, ok, "unexpected field in actual %q=%q", actualKey, actual[actualKey])
	}
}

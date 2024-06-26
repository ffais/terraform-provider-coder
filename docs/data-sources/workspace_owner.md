---
# generated by https://github.com/hashicorp/terraform-plugin-docs
page_title: "coder_workspace_owner Data Source - terraform-provider-coder"
subcategory: ""
description: |-
  Use this data source to fetch information about the workspace owner.
---

# coder_workspace_owner (Data Source)

Use this data source to fetch information about the workspace owner.



<!-- schema generated by tfplugindocs -->
## Schema

### Read-Only

- `email` (String) The email address of the user.
- `full_name` (String) The full name of the user.
- `groups` (List of String) The groups of which the user is a member.
- `id` (String) The UUID of the workspace owner.
- `name` (String) The username of the user.
- `oidc_access_token` (String) A valid OpenID Connect access token of the workspace owner. This is only available if the workspace owner authenticated with OpenID Connect. If a valid token cannot be obtained, this value will be an empty string.
- `session_token` (String) Session token for authenticating with a Coder deployment. It is regenerated every time a workspace is started.
- `ssh_private_key` (String, Sensitive) The user's generated SSH private key.
- `ssh_public_key` (String) The user's generated SSH public key.

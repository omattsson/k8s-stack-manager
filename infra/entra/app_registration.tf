resource "random_uuid" "role_admin" {}
resource "random_uuid" "role_user" {}

resource "azuread_application" "stack_manager" {
  display_name     = var.app_display_name
  sign_in_audience = "AzureADMyOrg"

  web {
    redirect_uris = var.redirect_uris
  }

  app_role {
    allowed_member_types = ["User"]
    description          = "Full administrative access to k8s-stack-manager"
    display_name         = "Admin"
    id                   = random_uuid.role_admin.result
    value                = "admin"
    enabled              = true
  }

  app_role {
    allowed_member_types = ["User"]
    description          = "Standard user access to k8s-stack-manager"
    display_name         = "User"
    id                   = random_uuid.role_user.result
    value                = "user"
    enabled              = true
  }

  required_resource_access {
    resource_app_id = "00000003-0000-0000-c000-000000000000" # Microsoft Graph

    resource_access {
      id   = "37f7f235-527c-4136-accd-4a02d197296e" # openid
      type = "Scope"
    }
    resource_access {
      id   = "14dad69e-099b-42c9-810b-d002981feec1" # profile
      type = "Scope"
    }
    resource_access {
      id   = "64a6cdd6-aab1-4aaf-94b8-3cc8405e90d0" # email
      type = "Scope"
    }
  }

  optional_claims {
    id_token {
      name = "email"
    }
    id_token {
      name = "preferred_username"
    }
  }
}

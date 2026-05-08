resource "azuread_app_role_assignment" "admin_groups" {
  for_each = toset(var.admin_group_object_ids)

  app_role_id         = azuread_application.stack_manager.app_role_ids["admin"]
  principal_object_id = each.value
  resource_object_id  = azuread_service_principal.stack_manager.object_id
}

resource "azuread_app_role_assignment" "user_groups" {
  for_each = toset(var.user_group_object_ids)

  app_role_id         = azuread_application.stack_manager.app_role_ids["user"]
  principal_object_id = each.value
  resource_object_id  = azuread_service_principal.stack_manager.object_id
}

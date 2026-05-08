resource "azuread_application_password" "dev" {
  application_id = azuread_application.stack_manager.id
  display_name   = "docker-compose-dev"
  end_date       = timeadd(plantimestamp(), "8760h") # 1 year
}

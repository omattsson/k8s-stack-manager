package models

import (
	"errors"
)

var (
	ErrEmptyUsername = errors.New("username cannot be empty")
	ErrInvalidPrice  = errors.New("price must be positive")
	ErrEmptyItemName = errors.New("item name cannot be empty")
)

// Validate implements model validation for User.
func (u *User) Validate() error {
	if u.Username == "" {
		return ErrEmptyUsername
	}
	return nil
}

// Validate implements model validation for Item.
func (i *Item) Validate() error {
	if i.Name == "" {
		return ErrEmptyItemName
	}

	if i.Price <= 0 {
		return ErrInvalidPrice
	}

	return nil
}

// Validate implements model validation for StackTemplate.
func (t *StackTemplate) Validate() error {
	if t.Name == "" {
		return errors.New("name is required")
	}
	if t.OwnerID == "" {
		return errors.New("owner_id is required")
	}
	return nil
}

// Validate implements model validation for TemplateChartConfig.
func (c *TemplateChartConfig) Validate() error {
	if c.StackTemplateID == "" {
		return errors.New("stack_template_id is required")
	}
	if c.ChartName == "" {
		return errors.New("chart_name is required")
	}
	return nil
}

// Validate implements model validation for StackDefinition.
func (d *StackDefinition) Validate() error {
	if d.Name == "" {
		return errors.New("name is required")
	}
	if d.OwnerID == "" {
		return errors.New("owner_id is required")
	}
	return nil
}

// Validate implements model validation for ChartConfig.
func (c *ChartConfig) Validate() error {
	if c.StackDefinitionID == "" {
		return errors.New("stack_definition_id is required")
	}
	if c.ChartName == "" {
		return errors.New("chart_name is required")
	}
	return nil
}

// Validate implements model validation for StackInstance.
func (si *StackInstance) Validate() error {
	if si.StackDefinitionID == "" {
		return errors.New("stack_definition_id is required")
	}
	if si.Name == "" {
		return errors.New("name is required")
	}
	if si.OwnerID == "" {
		return errors.New("owner_id is required")
	}
	return nil
}

// Validate implements model validation for ValueOverride.
func (v *ValueOverride) Validate() error {
	if v.StackInstanceID == "" {
		return errors.New("stack_instance_id is required")
	}
	if v.ChartConfigID == "" {
		return errors.New("chart_config_id is required")
	}
	return nil
}

// Validate implements model validation for AuditLog.
func (a *AuditLog) Validate() error {
	if a.UserID == "" {
		return errors.New("user_id is required")
	}
	if a.Action == "" {
		return errors.New("action is required")
	}
	if a.EntityType == "" {
		return errors.New("entity_type is required")
	}
	return nil
}

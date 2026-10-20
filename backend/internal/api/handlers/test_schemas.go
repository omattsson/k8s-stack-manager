package handlers

// Schema for a single item
const itemSchema = `{
"type": "object",
"required": ["id", "name", "price"],
"properties": {
"id": {
"type": "integer",
"minimum": 1
},
"name": {
"type": "string",
"minLength": 1
},
"price": {
"type": "number",
"minimum": 0
}
}
}`

// Schema for an array of items
const itemListSchema = `{
"type": "array",
"items": ` + itemSchema + `
}`

// actionRunListSchema is the contract of GET /stack-instances/{id}/action-runs
// (#640). additionalProperties=false makes sure that no URL, result body or
// other field leaks into a row.
const actionRunListSchema = `{
"type": "object",
"required": ["instance_id", "can_view_logs", "data"],
"additionalProperties": false,
"properties": {
"instance_id": {"type": "string", "minLength": 1},
"can_view_logs": {"type": "boolean"},
"next_cursor": {"type": "string", "minLength": 1},
"data": {
"type": "array",
"items": {
"type": "object",
"required": ["id", "instance_id", "action", "action_label", "user_id", "started_at", "parameters", "status_code", "has_job_log"],
"additionalProperties": false,
"properties": {
"id": {"type": "string", "minLength": 1},
"instance_id": {"type": "string", "minLength": 1},
"action": {"type": "string", "minLength": 1},
"action_label": {"type": "string", "minLength": 1},
"user_id": {"type": "string"},
"started_by": {"type": "string", "minLength": 1},
"started_at": {"type": "string", "format": "date-time"},
"finished_at": {"type": "string", "format": "date-time"},
"parameters": {"type": "object", "additionalProperties": {"type": ["string", "boolean"]}},
"status_code": {"type": "integer", "minimum": 0},
"message": {"type": "string", "minLength": 1},
"job_id": {"type": "string", "minLength": 1},
"job_status": {"type": "string", "minLength": 1},
"has_job_log": {"type": "boolean"},
"request_id": {"type": "string", "minLength": 1}
}
}
}
}
}`

// Schema for error responses
const errorSchema = `{
"type": "object",
"required": ["error"],
"properties": {
"error": {
"type": "string"
}
}
}`

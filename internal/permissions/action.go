package permissions

// Action names the operation being authorized. The resolver maps each action
// to the minimum role required to permit it.
type Action string

const (
	ActionRead       Action = "read"        // Read a file or resource
	ActionWriteFile  Action = "write_file"  // Create a new file
	ActionEditFile   Action = "edit_file"   // Modify an existing file
	ActionDeleteFile Action = "delete_file" // Remove a file
	ActionCron       Action = "cron"        // Manage cron jobs
	ActionAdmin      Action = "admin"       // Administrative operations
)

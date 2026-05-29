package controllers

type Datapoints struct {
	Labels []string `json:"labels"`
	Data   []int64  `json:"data"`
}

type TemplateParams struct {
	Countries    Datapoints `json:"countries"`
	Passwords    Datapoints `json:"passwords"`
	Users        Datapoints `json:"users"`
	AuthAttempts Datapoints `json:"auth_attempts"`

	TotalCountries int64 `json:"total_countries"`
	TotalPasswords int64 `json:"total_passwords"`
	TotalUsers     int64 `json:"total_users"`
}

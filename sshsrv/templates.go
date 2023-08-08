package sshsrv

var tunnelSuccessfulBannerTemplate = `
###
### HTTP tunnel successfully created on {{.HTTP}}
{{- if .HTTPS}}
### HTTPS tunnel successfully created on {{.HTTPS}}
{{- end}}
###
`

package httpsrv

import (
	"bytes"
	"html/template"
)

var badRequestTemplate = `
<!DOCTYPE html>
<html>
<head>
	<title>Error 502 - {{.Hostname}}</title>
	<style>
		.center {
			text-align: center;
		}
    </style>

</head>

<body>
<div class="center">
        <h1>Error 502 - Bad Gateway</h1>
</div>
</body>
</html>
`
var tunnelSuccessfulBannerTemplate = `
### 
### HTTP tunnel successfully created on {{.HTTPURL}}
{{- if .UseTLS}}
### HTTPS tunnel successfully created on {{.HTTPSURL}}
{{- end}}
###
`

type bannerParams struct {
	HTTPURL  string
	HTTPSURL string
	UseTLS   bool
}

func badRequestHTML(hostname string) []byte {
	fallback := []byte("Bad Gateway")
	tpl, err := template.New("").Parse(badRequestTemplate)
	if err != nil {
		return fallback
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, struct{ Hostname string }{Hostname: hostname}); err != nil {
		return fallback
	}
	return buf.Bytes()
}

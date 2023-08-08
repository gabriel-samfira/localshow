// Copyright 2023 Gabriel Adrian Samfira
//
//    Licensed under the Apache License, Version 2.0 (the "License"); you may
//    not use this file except in compliance with the License. You may obtain
//    a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//    WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//    License for the specific language governing permissions and limitations
//    under the License.

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
### HTTP tunnel successfully created on {{.HTTP}}
{{- if .HTTPS}}
### HTTPS tunnel successfully created on {{.HTTPS}}
{{- end}}
###
`

type bannerParams struct {
	HTTPURL  string
	HTTPSURL string
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

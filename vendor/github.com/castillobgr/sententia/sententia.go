package sententia

import (
	"bytes"
	"text/template"
)

// Make generates a sentence replacing the noun and adjective templates.
func Make(sentence string) (string, error) {
	tmpl, err := template.New("sentence").Funcs(funcs).Parse(sentence)
	if err != nil {
		return "", err
	}
	var buf = &bytes.Buffer{}
	err = tmpl.Execute(buf, nil)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

// AddActions merges new actions to the default ones.
func AddActions(customActions map[string]interface{}) {
	for actionName, actionFunc := range customActions {
		funcs[actionName] = actionFunc
	}
}

// SetNouns replaces the built-in noun list with a custom one.
func SetNouns(customNouns []string) {
	nounList = customNouns
}

// AddNouns appends a custom noun list to the built-in one.
func AddNouns(customNouns []string) {
	nounList = append(nounList, customNouns...)
}

// SetAdjectives replaces the built-in adjective list with a custom one.
func SetAdjectives(customAdjectives []string) {
	adjectiveList = customAdjectives
}

// AddAdjectives appends a custom adjective list to the built-in one.
func AddAdjectives(customAdjectives []string) {
	adjectiveList = append(adjectiveList, customAdjectives...)
}

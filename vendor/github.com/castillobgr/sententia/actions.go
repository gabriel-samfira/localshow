package sententia

import (
	"fmt"
	"math/rand"
	"text/template"
	"time"
)

func init() {
	rand.Seed(time.Now().UTC().UnixNano())
}

var funcs = template.FuncMap{
	"noun":      noun,
	"nouns":     nouns,
	"adjective": adjective,
	"an":        articlize,
	"a":         articlize,
}

func noun() string {
	return nounList[rand.Intn(len(nounList))]
}

func nouns() string {
	return pluralize(noun())
}

func adjective() string {
	return adjectiveList[rand.Intn(len(adjectiveList))]
}

func articlize(word string) string {
	var article = "a"
	switch word[0] {
	case 'a', 'e', 'i', 'o', 'u':
		article = "an"
	}
	return fmt.Sprintf("%s %s", article, word)
}

func pluralize(word string) string {
	var suffix = "s"
	switch word[len(word)-1] {
	case 'y':
		word = fmt.Sprintf("%s%s", word[:len(word)-2], "i")
		fallthrough
	case 's', 'h':
		suffix = "es"
	}
	return fmt.Sprintf("%s%s", word, suffix)
}

package web

import "html/template"

func LoadTemplates(pattern string) (*template.Template, error) {
	return template.ParseGlob(pattern)
}

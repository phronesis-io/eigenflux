package index

import (
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

func Normalize(s string) string {
	return strings.Join(strings.Fields(cases.Fold().String(norm.NFKC.String(s))), " ")
}

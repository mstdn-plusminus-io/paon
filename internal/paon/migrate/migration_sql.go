package migrate

import "strings"

// splitSQLStatements handles the standard PostgreSQL dump syntax used by the
// embedded fresh-schema snapshot. In particular, semicolons inside the
// timestamp_id dollar-quoted function body and quoted values are not split.
func splitSQLStatements(source string) []string {
	statements := make([]string, 0, strings.Count(source, ";"))
	start := 0
	lineComment := false
	blockDepth := 0
	var quote byte
	dollarTag := ""
	for index := 0; index < len(source); index++ {
		current := source[index]
		if lineComment {
			if current == '\n' {
				lineComment = false
			}
			continue
		}
		if blockDepth > 0 {
			if current == '/' && index+1 < len(source) && source[index+1] == '*' {
				blockDepth++
				index++
			} else if current == '*' && index+1 < len(source) && source[index+1] == '/' {
				blockDepth--
				index++
			}
			continue
		}
		if dollarTag != "" {
			if strings.HasPrefix(source[index:], dollarTag) {
				index += len(dollarTag) - 1
				dollarTag = ""
			}
			continue
		}
		if quote != 0 {
			if current != quote {
				continue
			}
			if index+1 < len(source) && source[index+1] == quote {
				index++
				continue
			}
			quote = 0
			continue
		}
		if current == '-' && index+1 < len(source) && source[index+1] == '-' {
			lineComment = true
			index++
			continue
		}
		if current == '/' && index+1 < len(source) && source[index+1] == '*' {
			blockDepth = 1
			index++
			continue
		}
		if current == '\'' || current == '"' {
			quote = current
			continue
		}
		if current == '$' {
			if end := strings.IndexByte(source[index+1:], '$'); end >= 0 {
				end += index + 1
				tag := source[index : end+1]
				valid := true
				for position := 1; position < len(tag)-1; position++ {
					character := tag[position]
					if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_') {
						valid = false
						break
					}
				}
				if valid {
					dollarTag = tag
					index = end
					continue
				}
			}
		}
		if current == ';' {
			if statement := strings.TrimSpace(source[start : index+1]); statement != "" {
				statements = append(statements, statement)
			}
			start = index + 1
		}
	}
	if statement := strings.TrimSpace(source[start:]); statement != "" {
		statements = append(statements, statement)
	}
	return statements
}

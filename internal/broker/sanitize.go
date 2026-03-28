package broker

// Deterministic redaction after LLM shaping. Covers quoted/unquoted YAML secrets,
// Bearer tokens, URLs, emails, base64 blobs, Mongo-style IDs, internal TLD hosts,
// and *.croc.ru (extend reCorpHost or add config if your org uses other domains).

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

// sanitizeShaped applies deterministic redaction after the LLM shapes context.
// LLMs are unreliable at anonymization; this is the safety net.
func sanitizeShaped(s *shapedContext, logger *slog.Logger) {
	if s == nil {
		return
	}
	var n int
	s.Question = sanitizeText(s.Question, &n)
	s.CodeContext = sanitizeText(s.CodeContext, &n)
	s.Constraints = sanitizeText(s.Constraints, &n)
	if n > 0 && logger != nil {
		logger.Info("sanitize: deterministic redactions applied", "count", n)
	}
}

var (
	reBearer      = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._\-~+/=]{10,}`)
	reEmail       = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	reIPv4        = regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\b`)
	reURL         = regexp.MustCompile(`https?://[^\s"'<>)\]]+`)
	reBase64Token = regexp.MustCompile(`\b[A-Za-z0-9+/]{32,}={0,2}\b`)
	reMongoID     = regexp.MustCompile(`\b[0-9a-f]{24}\b`)
	reYAMLSecret    = regexp.MustCompile(`(?m)(?i)(api_key|apikey|token|password|secret|client_secret|access_token)\s*:\s*["']([^"']{8,})["']`)
	reYAMLUnquoted  = regexp.MustCompile(`(?m)(?i)(api_key|apikey|token|password|secret|client_secret|access_token)\s*:\s*([A-Za-z0-9+/=_\-]{16,})`)
	reProjHeader    = regexp.MustCompile(`(?i)(ContinueDevProject)\s*:\s*["']?([^"'\s\n#]+)["']?`)
	reInternalHost  = regexp.MustCompile(`\b[a-zA-Z0-9][-a-zA-Z0-9.]*\.(?:internal|local|corp|lan)\b`)
	reCorpHost      = regexp.MustCompile(`\b[a-zA-Z0-9][-a-zA-Z0-9.]*\.croc\.ru\b`)
)

func sanitizeText(in string, count *int) string {
	if in == "" {
		return in
	}

	var envVals []string
	out := in
	for i := 0; ; i++ {
		start := strings.Index(out, "${")
		if start < 0 {
			break
		}
		rest := out[start+2:]
		end := strings.Index(rest, "}")
		if end < 0 {
			break
		}
		end = start + 2 + end
		orig := out[start : end+1]
		ph := fmt.Sprintf("<<<ENV%d>>>", i)
		envVals = append(envVals, orig)
		out = out[:start] + ph + out[end+1:]
	}

	addCount := func(re *regexp.Regexp, before, after string) {
		if before != after {
			*count += len(re.FindAllString(before, -1))
		}
	}

	before := out
	out = reBearer.ReplaceAllString(out, "Bearer <REDACTED>")
	addCount(reBearer, before, out)

	before = out
	out = reEmail.ReplaceAllStringFunc(out, func(s string) string {
		ls := strings.ToLower(s)
		if strings.Contains(ls, "example.com") || strings.Contains(ls, "example.org") || strings.Contains(ls, "localhost") {
			return s
		}
		*count++
		return "user@example.com"
	})

	before = out
	out = reIPv4.ReplaceAllString(out, "10.0.0.0")
	addCount(reIPv4, before, out)

	before = out
	out = reURL.ReplaceAllStringFunc(out, func(s string) string {
		ls := strings.ToLower(s)
		if strings.Contains(ls, "example.com") || strings.Contains(ls, "example.org") {
			return s
		}
		*count++
		return "https://redacted.example"
	})

	before = out
	out = reBase64Token.ReplaceAllString(out, "<REDACTED>")
	addCount(reBase64Token, before, out)

	before = out
	out = reMongoID.ReplaceAllString(out, "<OBJECT_ID>")
	addCount(reMongoID, before, out)

	before = out
	out = reYAMLSecret.ReplaceAllStringFunc(out, func(m string) string {
		sm := reYAMLSecret.FindStringSubmatch(m)
		if len(sm) < 3 {
			return m
		}
		*count++
		return sm[1] + `: "<REDACTED>"`
	})

	before = out
	out = reYAMLUnquoted.ReplaceAllStringFunc(out, func(m string) string {
		sm := reYAMLUnquoted.FindStringSubmatch(m)
		if len(sm) < 3 {
			return m
		}
		if strings.HasPrefix(sm[2], `"`) || strings.HasPrefix(sm[2], `'`) {
			return m
		}
		*count++
		return sm[1] + `: "<REDACTED>"`
	})

	before = out
	out = reProjHeader.ReplaceAllStringFunc(out, func(m string) string {
		sm := reProjHeader.FindStringSubmatch(m)
		if len(sm) < 3 {
			return m
		}
		*count++
		return sm[1] + `: "<PROJECT_ID>"`
	})
	_ = before

	before = out
	out = reInternalHost.ReplaceAllString(out, "redacted.internal")
	addCount(reInternalHost, before, out)

	before = out
	out = reCorpHost.ReplaceAllString(out, "redacted.example")
	addCount(reCorpHost, before, out)

	for i, orig := range envVals {
		out = strings.ReplaceAll(out, fmt.Sprintf("<<<ENV%d>>>", i), orig)
	}

	return out
}

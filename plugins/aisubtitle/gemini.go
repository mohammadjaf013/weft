package aisubtitle

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var base64Std = base64.StdEncoding

// RefineSRT sends an SRT file to Gemini. When srcLang and dstLang differ the
// model translates the subtitles; when equal (or srcLang empty) it just
// proofreads the text. Timestamps are always preserved exactly.
func (geminiClient) RefineSRT(ctx context.Context, key, model, srcLang, dstLang, srt string) (string, error) {
	if model == "" {
		model = "gemini-1.5-flash"
	}
	var prompt string
	if srcLang != "" && dstLang != "" && srcLang != dstLang {
		prompt = "You are a professional subtitle translator. Translate the following " +
			"SRT subtitles from " + srcLang + " to " + dstLang + ". Keep every " +
			"timestamp line EXACTLY as-is, keep one cue per block, and translate " +
			"naturally (names may stay transliterated). Return only the SRT, nothing else.\n\n" + srt
	} else {
		prompt = "You are a subtitle proofreader. Fix grammar, punctuation and " +
			"natural wording of the following SRT subtitles (language: " + dstLang + "). " +
			"Keep every timestamp line EXACTLY as-is and keep one cue per block. " +
			"Return only the corrected SRT, nothing else.\n\n" + srt
	}
	req := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{"text": prompt},
				},
			},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	return postJSON(ctx, "https://generativelanguage.googleapis.com/v1beta/models/"+model+":generateContent?key="+key, body)
}

// postJSON issues a POST with JSON body and returns the response body.
func postJSON(ctx context.Context, url string, body []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini request failed: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("gemini returned %d: %s", resp.StatusCode, truncate(b))
	}
	// parse candidates[0].content.parts[0].text
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini returned no transcript")
	}
	return out.Candidates[0].Content.Parts[0].Text, nil
}

func truncate(b []byte) string {
	s := string(b)
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

// plainToSRT wraps a plain-text transcript into an SRT file with one long cue.
func plainToSRT(text string) string {
	lines := strings.Fields(text)
	if len(lines) == 0 {
		return "1\n00:00:00,000 --> 00:00:01,000\n\n"
	}
	return "1\n00:00:00,000 --> 00:01:00,000\n" + strings.Join(lines, " ") + "\n"
}

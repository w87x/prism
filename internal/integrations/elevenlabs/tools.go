package elevenlabs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/tools"
)

func marker(kind string, id int64) string { return fmt.Sprintf("[%s:%d]", kind, id) }

// RegisterTools installs tts_speak, sound_effect, image_generate and voice_list.
func RegisterTools(reg *tools.Registry, s *Service) {
	reg.Register(
		&tools.Tool{
			Name: "tts_speak", Category: "voice", Risk: tools.RiskWrite,
			Description: "Turn text into spoken audio (ElevenLabs) and save it as an mp3. Spends the user's credits (~1 per character, 0.5 on Flash models), so speak only what is worth hearing — short replies, summaries, reminders. The result contains a marker like [audio:12]: put it in your reply so the user gets a player. Use voice_list for voice names.",
			Params:      tools.Obj("text", tools.Str("text", "what to say (under 5000 characters)"), tools.Str("voice", "voice name or id (default: the configured voice)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Text, Voice string }](raw)
				if err != nil {
					return "", err
				}
				if n := len([]rune(a.Text)); n > s.cfg(ctx).ConfirmOver && s.cfg(ctx).ConfirmOver > 0 {
					if err := tools.Confirm(ctx, env, "tts_speak", fmt.Sprintf("%d characters", n),
						fmt.Sprintf("%s wants to speak %d characters with ElevenLabs (about %d credits).", env.Agent, n, int64(float64(n)*costPerChar(s.cfg(ctx).ModelID)))); err != nil {
						return "", err
					}
				}
				m, err := s.Speak(ctx, a.Text, a.Voice, env.Agent)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Audio ready: %s — voice %s, %d bytes, ~%d credits (~%d left this cycle). Include %s in your reply so the user can play it.",
					marker("audio", m.ID), m.Voice, m.Bytes, m.Credit, m.Left, marker("audio", m.ID)), nil
			},
		},
		&tools.Tool{
			Name: "sound_effect", Category: "voice", Risk: tools.RiskWrite,
			Description: "Generate a sound effect or short ambience from a description (ElevenLabs), saved as mp3. Costs credits (~40 per second). Returns a marker like [audio:12] to include in your reply.",
			Params:      tools.Obj("prompt", tools.Str("prompt", "the sound, e.g. \"rain on a tin roof, distant thunder\""), tools.Num("duration_s", "seconds, 0.5–22 (omit to let the model decide)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Prompt    string
					DurationS float64 `json:"duration_s"`
				}](raw)
				if err != nil {
					return "", err
				}
				m, err := s.SoundEffect(ctx, a.Prompt, a.DurationS, env.Agent)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Sound ready: %s (~%d credits, ~%d left this cycle). Include %s in your reply so the user can play it.",
					marker("audio", m.ID), m.Credit, m.Left, marker("audio", m.ID)), nil
			},
		},
		&tools.Tool{
			Name: "image_generate", Category: "voice", Risk: tools.RiskWrite,
			Description: "Generate an image from a description (ElevenLabs image API; requires a paid Pro plan on the user's ElevenLabs account — on the free plan it fails and you should say so). Returns a marker like [image:12] to include in your reply so the user sees it.",
			Params: tools.Obj("prompt", tools.Str("prompt", "what the image should show, in detail"),
				tools.Enum("aspect_ratio", "shape (default: model decides)", "1:1", "3:2", "2:3", "4:3", "3:4", "16:9", "9:16", "21:9"),
				tools.Enum("resolution", "size (model-dependent)", "1K", "2K", "4K"),
				tools.Str("model", "image model id (default: the configured one)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Prompt, Resolution, Model string
					AspectRatio               string `json:"aspect_ratio"`
				}](raw)
				if err != nil {
					return "", err
				}
				m, err := s.Image(ctx, a.Prompt, a.Model, a.AspectRatio, a.Resolution, env.Agent)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Image ready: %s (%d bytes). Include %s in your reply so the user sees it.", marker("image", m.ID), m.Bytes, marker("image", m.ID)), nil
			},
		},
		&tools.Tool{
			Name: "voice_list", Category: "voice", Risk: tools.RiskRead,
			Description: "List the ElevenLabs voices available to the user (name, gender/accent/use-case labels).",
			Params:      tools.Obj(""),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				vs, err := s.Voices(ctx, "")
				if err != nil {
					return "", err
				}
				var sb strings.Builder
				for _, v := range vs {
					var lab []string
					for _, k := range []string{"gender", "accent", "age", "use_case", "descriptive"} {
						if x := v.Labels[k]; x != "" {
							lab = append(lab, x)
						}
					}
					fmt.Fprintf(&sb, "- %s [%s] %s\n", v.Name, v.Category, strings.Join(lab, ", "))
				}
				if sb.Len() == 0 {
					return "No voices available.", nil
				}
				return sb.String(), nil
			},
		},
	)
}

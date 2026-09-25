---
name: transcribing-audio
description: Use when a task requires STT/ASR.
when: exe.dev
---

## Steps

1. By default, save the transcript beside the original audio with the extension replaced by `.transcript.txt` (`notes/review.m4a` → `notes/review.transcript.txt`).

2. Check the input. Transcription accepts mp3, mp4, mpeg, mpga, m4a, wav, webm, flac, and ogg, up to 25 MB. Do not run `ffprobe` or inspect duration for an existing local file with a supported extension under that limit.

   Transcode unsupported or larger inputs using ffmpeg. Split and transcribe piecemeal if necessary; for better results, slightly overlap the chunks and then manually stitch together the overlapped outputs. Shelley browser screen recordings have a sibling `<recording-path>.json` sidecar with `path` and `duration_ms`. Preserve each split chunk's media start offset so chunk-relative timestamps can be rolled up to the original recording timeline.

3. Prefer Shelley's selected transcription models. This keeps agent-run transcription aligned with the defaults chosen in the Models modal and lets Shelley use stored custom-provider credentials without exposing them.

   - Use the `timecoded` default when the user asks for timestamps or continues a timestamp task. Otherwise use `prompted`.
   - Read `GET /api/transcription-models` from the running Shelley. For localhost, include `X-Exedev-Userid` with the exe.dev owner email from system context.
   - Resolve `defaults[role].model_id` against `models`. If the default is available, POST that model and the recording to `/api/transcription-models/test`, enabling only the requested role in the submitted draft.
   - Save `results[role].transcript` to the transcript path. For `timecoded`, save `results[role].timecodes` to the sibling `.timestamps.json`.
   - Report the selected model's display name. Do not silently call a different provider.

   Example:
   ```
   shelley_url=${SHELLEY_URL:-http://localhost:9999}
   auth=(-H "X-Exedev-Userid: $owner_email")
   catalog=$(curl -fsS "${auth[@]}" "$shelley_url/api/transcription-models")
   model_id=$(jq -er --arg role "$role" '.defaults[$role] | select(.available) | .model_id' <<<"$catalog")
   draft=$(jq -cer --arg id "$model_id" --arg role "$role" '
     .models[] | select(.model_id == $id)
     | .supports_prompted = ($role == "prompted")
     | .supports_timecodes = ($role == "timecoded")
   ' <<<"$catalog")
   curl -fsS "${auth[@]}" "$shelley_url/api/transcription-models/test" \
     -F "model=$draft" -F "prompt=$prompt" -F "file=@$upload" > "$tmpdir/response.json"
   jq -er --arg role "$role" '.results[$role] | select(.success) | .transcript' \
     "$tmpdir/response.json" > "$out"
   if [ "$role" = timecoded ]; then
     timestamp_out="${out%.transcript.txt}.timestamps.json"
     jq -e --arg role "$role" '.results[$role].timecodes' \
       "$tmpdir/response.json" > "$timestamp_out"
   fi
   ```

4. If there is no running Shelley, no available selected model for the requested role, or the catalog endpoint is unavailable, discover the VM's attached LLM integration through Reflection. State this fallback explicitly in the final response.

   Prefer an integration named `llm`; otherwise use the first attached integration whose type is `llm`. Its endpoint is `https://<name>.int.exe.xyz`. Do not assume that an integration named `openai` exists, and do not cascade across guessed hostnames. If Reflection reports no LLM integration, stop and report that transcription is not configured.

   A JSON response format is required. For `gpt-transcribe`, optional `prompt`, `keywords[]`, and `languages[]` fields can supply known context, names, and language codes.
   ```
   integration=$(curl -fsS https://reflection.int.exe.xyz/integrations | jq -er '
     [.integrations[] | select(.type == "llm") | .name]
     | (map(select(. == "llm")) + map(select(. != "llm")))
     | first
   ')
   base="https://$integration.int.exe.xyz"
   curl -sS --fail-with-body "$base/v1/audio/transcriptions" \
     -F model=gpt-transcribe -F response_format=json -F "file=@$upload" \
     -o "$tmpdir/response.json"
   jq -er '.text | select(type == "string")' "$tmpdir/response.json" > "$out"
   ```

   When the user asks for word or segment timestamps, run the GPT command
   above and the Whisper command below as two parallel bash tool calls in one
   response. The GPT transcript stays canonical; Whisper's verbose JSON supplies timing only.
   Whisper accepts a singular `language`; although its API also accepts `prompt`, Shelley reserves context prompting for the canonical GPT transcription. The direct Whisper timecode request must not send a prompt.
   ```
   timestamp_out="${out%.transcript.txt}.timestamps.json"
   curl -sS --fail-with-body "$base/v1/audio/transcriptions" \
     -F model=whisper-1 -F response_format=verbose_json \
     -F 'timestamp_granularities[]=word' -F 'timestamp_granularities[]=segment' \
     -F "file=@$upload" -o "$timestamp_out"
   jq -e '(.words | type == "array") and (.segments | type == "array")' "$timestamp_out" >/dev/null
   ```

5. Report the selected model and output paths, then stop.

## Errors

- `402`: LLM credits exhausted; https://exe.dev/user/shelley.
- The fallback LLM integration must expose OpenAI-compatible audio transcription models.

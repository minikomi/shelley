---
name: transcribing-audio
description: Use when a task requires STT/ASR.
when: exe.dev
---

## Steps

1. By default, save the transcript beside the original audio with the extension replaced by `.transcript.txt` (`notes/review.m4a` → `notes/review.transcript.txt`).

2. Check the input. The gpt-transcribe endpoint accepts mp3, mp4, mpeg, mpga, m4a, wav, webm, flac, and ogg, up to 25 MB. For an existing local file with a supported extension under that limit, use the fast path below immediately. Do not run `ffprobe`, inspect duration, list models, probe endpoints, load `reflection-integration`, or query available integrations first.

   Transcode unsupported or larger inputs using ffmpeg. Split and transcribe piecemeal if necessary; for better results, slightly overlap the chunks and then manually stitch together the overlapped outputs. Shelley browser recordings have a sibling `<recording-path>.json` sidecar with `started_at`, `duration_ms`, and `timeslice_ms`. Preserve each split chunk's media start offset so any chunk-relative timestamps can be rolled up to the original recording timeline; `started_at` anchors that timeline to wall-clock time.

3. Transcribe. Try the OpenAI-compatible gateways in this fixed order and keep the first that succeeds: `https://openai.int.exe.xyz`, then `https://llm.int.exe.xyz`. Do not look up integrations first; the transcription response is the only reliable signal.

   A JSON response format is required. For `gpt-transcribe`, optional `prompt`, `keywords[]`, and `languages[]` fields can supply known context, names, and language codes.
   ```
   transcribe() {
     for base in https://openai.int.exe.xyz https://llm.int.exe.xyz; do
       curl -sS --fail-with-body "$base/v1/audio/transcriptions" "$@" && return
     done
     return 1
   }
   transcribe -F model=gpt-transcribe -F response_format=json -F "file=@$upload" -o "$tmpdir/response.json"
   jq -er '.text | select(type == "string")' "$tmpdir/response.json" > "$out"
   ```

   When the user asks for word or segment timestamps, run the GPT command
   above and the Whisper command below as two parallel bash tool calls in one
   response; each call defines `transcribe` and its own variables. The GPT
   transcript stays canonical; Whisper's verbose JSON supplies timing only.
   Whisper accepts `prompt` and singular `language`, not the `gpt-transcribe`
   keyword and language arrays.
   ```
   timestamp_out="${out%.transcript.txt}.timestamps.json"
   transcribe -F model=whisper-1 -F response_format=verbose_json \
     -F 'timestamp_granularities[]=word' -F 'timestamp_granularities[]=segment' \
     -F "file=@$upload" -o "$timestamp_out"
   jq -e '(.words | type == "array") and (.segments | type == "array")' "$timestamp_out" >/dev/null
   ```

4. Report the output paths and stop.

## Errors

- `402`: LLM credits exhausted; https://exe.dev/user/shelley.
- Transcription requires managed OpenAI or OpenAI BYOK; ChatGPT subscriptions return `400` on this path.
- If every gateway fails, show the last error. If it reports a missing integration, use `request-integration` to provide the connect link and stop. Never ask the user to paste a secret.

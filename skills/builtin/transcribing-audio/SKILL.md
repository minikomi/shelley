---
name: transcribing-audio
description: Use when a task requires STT/ASR.
when: exe.dev
---

## Steps

1. By default, save the transcript beside the original audio with the extension replaced by `.transcript.txt` (`notes/review.m4a` → `notes/review.transcript.txt`).

2. Check the input. The gpt-transcribe endpoint accepts mp3, mp4, mpeg, mpga, m4a, wav, webm, flac, and ogg, up to 25 MB. For an existing local file with a supported extension under that limit, use the fast path below immediately. Do not run `ffprobe`, inspect duration, list models, probe endpoints, load `reflection-integration`, or query available integrations first.

   Transcode unsupported or larger inputs using ffmpeg. Split and transcribe piecemeal if necessary; for better results, slightly overlap the chunks and then manually stitch together the overlapped outputs. Shelley browser recordings have a sibling `<recording-path>.json` sidecar with `started_at`, `duration_ms`, and `timeslice_ms`. Preserve each split chunk's media start offset so any chunk-relative timestamps can be rolled up to the original recording timeline; `started_at` anchors that timeline to wall-clock time.

3. Transcribe through the OpenAI integration at `https://openai.int.exe.xyz`. Call it directly. Do not try `https://llm.int.exe.xyz` or a ChatGPT-backed gateway as a fallback.

   A JSON response format is required. For `gpt-transcribe`, optional `prompt`, `keywords[]`, and `languages[]` fields can supply known context, names, and language codes.
   ```
   base=https://openai.int.exe.xyz
   curl -sS --fail-with-body "$base/v1/audio/transcriptions" \
     -F model=gpt-transcribe \
     -F response_format=json \
     -F "file=@$upload" \
     -o "$tmpdir/response.json"
   jq -er '.text | select(type == "string")' "$tmpdir/response.json" > "$out"
   ```

   When the user asks for word or segment timestamps, issue the GPT command
   above and the Whisper command below as two parallel bash tool calls in the
   same assistant response. Each call must define its own paths and variables;
   shell state is not shared between tool calls. Wait for both calls before
   replying. Keep the GPT transcript as the canonical transcript and use
   Whisper's verbose JSON only for timing. Whisper accepts `prompt` and singular
   `language`, rather than the `gpt-transcribe`-specific keyword and language
   arrays. Preserve the full JSON beside the transcript; do not replace the GPT
   transcript with Whisper's text.
   ```
   timestamp_out="${out%.transcript.txt}.timestamps.json"
   curl -sS --fail-with-body "$base/v1/audio/transcriptions" \
     -F model=whisper-1 \
     -F response_format=verbose_json \
     -F 'timestamp_granularities[]=word' \
     -F 'timestamp_granularities[]=segment' \
     -F "file=@$upload" \
     -o "$timestamp_out"
   jq -e '(.words | type == "array") and (.segments | type == "array")' "$timestamp_out" >/dev/null
   ```

4. After the output files are written, report their paths and stop. Do not reread or reformat the complete responses unless the user asks.

## Errors

- `402`: LLM credits exhausted; https://exe.dev/user/shelley.
- Transcription requires managed OpenAI or OpenAI BYOK; ChatGPT subscriptions and ChatGPT-backed gateways don't support this path.
- If the direct OpenAI request reports that the integration is absent, use `request-integration` to provide the integration-connect link and stop. Never ask the user to paste a secret or perform integration discovery before the request.

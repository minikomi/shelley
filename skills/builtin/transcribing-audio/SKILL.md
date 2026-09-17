---
name: transcribing-audio
description: Use when a task requires STT/ASR.
when: exe.dev
---

## Steps

1. By default, save the transcript beside the original audio with the extension replaced by `.transcript.txt` (`notes/review.m4a` → `notes/review.transcript.txt`).

2. Check the input. The gpt-transcribe endpoint accepts mp3, mp4, mpeg, mpga, m4a, wav, webm, flac, and ogg, up to 25 MB. Transcode unsupported or larger inputs first using ffmpeg. Split and transcribe piecemeal if necessary; for better results, slightly overlap the chunks and then manually stitch together the overlapped outputs. Shelley browser recordings have a sibling `<recording-path>.json` sidecar with `started_at`, `duration_ms`, and `timeslice_ms`. Preserve each split chunk's media start offset so any chunk-relative timestamps can be rolled up to the original recording timeline; `started_at` anchors that timeline to wall-clock time.

3. Transcribe. Let `$base` be the attached integration's URL (normally `https://llm.int.exe.xyz`). A JSON response format is required. For `gpt-transcribe`, optional `prompt`, `keywords[]`, and `languages[]` fields can supply known context, names, and language codes.
   ```
   curl -sS --fail-with-body "$base/v1/audio/transcriptions" \
     -F model=gpt-transcribe \
     -F response_format=json \
     -F "file=@$upload" \
     -o "$tmpdir/response.json"
   jq -er '.text | select(type == "string")' "$tmpdir/response.json" > "$out"
   ```

   When the user asks for word or segment timestamps, keep the GPT transcript
   above as the canonical transcript and additionally use Whisper's verbose
   JSON response for timing. Whisper accepts `prompt` and singular `language`,
   rather than the `gpt-transcribe`-specific keyword and language arrays.
   Preserve the full JSON beside the transcript so the timing data is not lost;
   do not replace the GPT transcript with Whisper's text.
   ```
   timestamp_out="${out%.transcript.txt}.timestamps.json"
   curl -sS --fail-with-body "$base/v1/audio/transcriptions" \
     -F model=whisper-1 \
     -F response_format=verbose_json \
     -F 'timestamp_granularities[]=word' \
     -F 'timestamp_granularities[]=segment' \
     -F "file=@$upload" \
     -o "$timestamp_out"
   ```

## Errors

- `402`: LLM credits exhausted; https://exe.dev/user/shelley.
- Transcription requires managed OpenAI or OpenAI BYOK; ChatGPT subscriptions don't support it. A separate integration can provide transcription without changing the existing chat source.
- To find or connect a suitable integration, use `reflection-integration` and `request-integration`.

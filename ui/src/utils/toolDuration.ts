function formatWholeSeconds(totalSeconds: number): string {
  const seconds = totalSeconds % 60;
  const totalMinutes = Math.floor(totalSeconds / 60);
  const minutes = totalMinutes % 60;
  const hours = Math.floor(totalMinutes / 60);

  if (hours > 0) return `${hours}h ${minutes}m ${seconds}s`;
  if (totalMinutes > 0) return `${totalMinutes}m ${seconds}s`;
  return `${seconds}s`;
}

export function formatRunningToolDuration(ms: number): string {
  return formatWholeSeconds(Math.max(0, Math.floor(ms / 1000)));
}

export function formatFinishedToolDuration(ms: number): string {
  if (ms < 1000) return "<1s";
  return formatWholeSeconds(Math.round(ms / 1000));
}

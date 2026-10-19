// latestGate.ts — newest-request-wins for overlapping async loads.
//
// begin() starts a load and returns a check that stays true only until a newer
// load begins or supersede() runs. A load applies its result only while its
// check is true, so a slow older response can never overwrite newer data.
export interface LatestGate {
  begin: () => () => boolean;
  supersede: () => void;
}

export function latestGate(): LatestGate {
  let current = 0;
  return {
    begin() {
      const mine = ++current;
      return () => mine === current;
    },
    supersede() {
      current++;
    },
  };
}

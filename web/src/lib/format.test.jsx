import { describe, expect, it } from 'vitest';
import { redName, fmtDateTime, fmtDateTimeSec } from './format.js';

// Prefer operator-supplied restore-point display names, falling back to ASCII snapshot names.
describe('Restore-point display names', () => {
  it('Prefers operator-supplied names', () => {
    expect(redName({ Name: '@office', DisplayName: 'Office installed' })).toBe('Office installed');
  });

  it('Falls back to snapshot name for legacy records', () => {
    expect(redName({ Name: '@r1', DisplayName: '' })).toBe('@r1');
    expect(redName({ Name: '@r1' })).toBe('@r1');
  });

  it('Uses an empty string rather than undefined when absent', () => {
    expect(redName(null)).toBe('');
    expect(redName(undefined)).toBe('');
  });
});

// Unfinished jobs have null or Go zero timestamps; never display dates in 1970 or 0001.
describe("Missing timestamps display as —", () => {
  for (const v of [null, undefined, "", "0001-01-01T00:00:00Z"]) {
    it(String(v), () => {
      expect(fmtDateTime(v)).toBe("—");
      expect(fmtDateTimeSec(v)).toBe("—");
    });
  }
});

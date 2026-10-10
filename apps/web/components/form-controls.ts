/**
 * Classes for the native controls the UI package has no primitive for, and
 * the height every text field in the console shares, so a form looks the
 * same on settings, billing, account and the rest.
 */
export const fieldHeight = "h-10"

export const selectClass =
  "h-10 max-w-full rounded-md border border-input bg-background px-3 text-sm text-foreground outline-none transition-colors focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 dark:bg-input/30"

export const checkboxClass =
  "size-4 shrink-0 rounded-sm accent-primary outline-none focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50"

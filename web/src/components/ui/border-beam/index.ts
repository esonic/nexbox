/**
 * Vendored from `border-beam` v1.3.0 (MIT, © Jakub Antalik).
 * Source: https://github.com/Jakubantalik/border-beam — see ./LICENSE.
 *
 * Vendored (rather than npm-installed) so we own the CSS generators. Local
 * change: the `line` preset's `beam-travel` / `beam-edge-fade` keyframes were
 * widened so the bottom glow sweeps the full width, corner to corner, instead
 * of only lighting the middle third (see ./styles.ts, generateLineVariantCSS).
 */
export { BorderBeam } from './BorderBeam';
export { default } from './BorderBeam';

export type {
  BorderBeamProps,
  BorderBeamSize,
  BorderBeamTheme,
  BorderBeamColorVariant,
  SizeConfig,
  ThemeColors,
} from './types';

export { sizePresets, sizeThemePresets, themeColors } from './styles';

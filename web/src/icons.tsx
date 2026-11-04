// Stroke icons shared by the UI. All are decorative: pair them with text or an aria-label.

import { useId } from "react";

type P = { size?: number };

const svg = (size: number, children: React.ReactNode, viewBox = "0 0 16 16", strokeWidth = 1.6) => (
  <svg width={size} height={size} viewBox={viewBox} fill="none" stroke="currentColor" strokeWidth={strokeWidth}
    strokeLinecap="round" strokeLinejoin="round" aria-hidden>
    {children}
  </svg>
);

export const FolderIcon = ({ size = 15 }: P) =>
  svg(size, <path d="M1.75 4.25a1 1 0 0 1 1-1h3.4l1.5 1.5h5.6a1 1 0 0 1 1 1v6.5a1 1 0 0 1-1 1H2.75a1 1 0 0 1-1-1z" />, "0 0 16 16", 1.4);

export const FolderPlusIcon = ({ size = 15 }: P) =>
  svg(size, <>
    <path d="M1.75 4.25a1 1 0 0 1 1-1h3.4l1.5 1.5h5.6a1 1 0 0 1 1 1v6.5a1 1 0 0 1-1 1H2.75a1 1 0 0 1-1-1z" />
    <path d="M8 7.5v3.5M6.25 9.25h3.5" />
  </>, "0 0 16 16", 1.4);

export const PlusIcon = ({ size = 15 }: P) => svg(size, <path d="M8 3.5v9M3.5 8h9" />);

export const TrashIcon = ({ size = 15 }: P) =>
  svg(size, <path d="M2.75 4.5h10.5M6.25 4.5V3h3.5v1.5M4.25 4.5l.6 8.5h6.3l.6-8.5M6.75 7v4M9.25 7v4" />, "0 0 16 16", 1.5);

export const GearIcon = ({ size = 16 }: P) =>
  svg(size, <>
    <path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
    <circle cx="12" cy="12" r="3" />
  </>, "0 0 24 24", 1.8);

export const PlayIcon = ({ size = 13 }: P) => (
  <svg width={size} height={size} viewBox="0 0 16 16" fill="currentColor" aria-hidden>
    <path d="M4.5 2.8v10.4a.6.6 0 0 0 .9.5l8.3-5.2a.6.6 0 0 0 0-1L5.4 2.3a.6.6 0 0 0-.9.5z" />
  </svg>
);

export const ChevronDownIcon = ({ size = 16 }: P) => svg(size, <path d="M5 6.5 8 9.5l3-3" />);

export const ChevronRightIcon = ({ size = 12 }: P) => svg(size, <path d="M6 4l4 4-4 4" />);

export const SearchIcon = ({ size = 15 }: P) => svg(size, <><circle cx="7" cy="7" r="4.5" /><path d="m10.5 10.5 3 3" /></>);

export const CloseIcon = ({ size = 18 }: P) => svg(size, <path d="m4 4 8 8M12 4l-8 8" />);

export const CheckIcon = ({ size = 13 }: P) => svg(size, <path d="M3.5 8.5l3 3 6-7" />);

export const ExternalIcon = ({ size = 13 }: P) =>
  svg(size, <path d="M9 2.5h4.5V7M13.5 2.5 7 9M11.5 9.5v3a1 1 0 0 1-1 1h-7a1 1 0 0 1-1-1v-7a1 1 0 0 1 1-1h3" />, "0 0 16 16", 1.5);

export const PeopleIcon = ({ size = 13 }: P) =>
  svg(size, <path d="M1.5 13.5c.6-2.5 2.4-3.75 4.5-3.75s3.9 1.25 4.5 3.75M11 3.5a2.4 2.4 0 0 1 0 4.5M12.5 9.9c1 .5 1.7 1.6 2 3.6M6 8a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5z" />, "0 0 16 16", 1.5);

const LOGO_STARS = [
  [30, 30, 2, 0.9], [78, 54, 1.4, 0.7], [206, 30, 2, 0.9], [232, 76, 1.5, 0.7],
  [160, 22, 1.4, 0.6], [44, 96, 1.6, 0.7], [112, 28, 1.2, 0.6], [190, 88, 1.2, 0.5],
];
const LOGO_CONTOURS = [
  [224, "#C4476E", 0.55], [212, "#43306F", 0.9], [196, "#43306F", 0.75], [176, "#43306F", 0.6], [152, "#43306F", 0.45],
] as const;
const LOGO_GRID = [-40, 20, 76, 128, 180, 236, 296];

// "Primeira luz": the sun at the planet's edge. Below 48 px the stars and terrain lines would
// turn to noise, so small sizes draw a bolder limb and flare (the same drawing as favicon.svg).
export function Logo({ size = 22 }: P) {
  const uid = useId().replace(/[^a-zA-Z0-9]/g, "");
  const id = (name: string) => `logo-${uid}-${name}`;
  const url = (name: string) => `url(#${id(name)})`;
  const small = size < 48;
  const horizon = small ? 150 : 136;
  const flareY = small ? 146 : 134;
  const planetY = horizon + 232;
  return (
    <svg width={size} height={size} viewBox="0 0 256 256" aria-hidden>
      <defs>
        <clipPath id={id("tile")}>
          <rect width="256" height="256" rx="52" />
        </clipPath>
        <clipPath id={id("planet-clip")}>
          <circle cx="128" cy={planetY} r="232" />
        </clipPath>
        <linearGradient id={id("sky")} gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="0" y2="200">
          <stop offset="0" stopColor="#090A1A" />
          <stop offset="1" stopColor="#1E1B44" />
        </linearGradient>
        <radialGradient id={id("planet")} gradientUnits="userSpaceOnUse" cx="128" cy={horizon} r="200">
          <stop offset="0" stopColor="#3A2558" />
          <stop offset="0.5" stopColor="#191736" />
          <stop offset="1" stopColor="#0B0C1C" />
        </radialGradient>
        <radialGradient id={id("limb")} gradientUnits="userSpaceOnUse" cx="128" cy={horizon} r="140">
          <stop offset="0" stopColor="#FFF8E1" />
          <stop offset="0.25" stopColor="#FFC94D" />
          <stop offset="0.55" stopColor="#F0703E" />
          <stop offset="0.85" stopColor="#C4476E" stopOpacity="0.6" />
          <stop offset="1" stopColor="#C4476E" stopOpacity="0" />
        </radialGradient>
        <radialGradient id={id("flare")} gradientUnits="userSpaceOnUse" cx="128" cy={flareY} r="120">
          <stop offset="0" stopColor="#FFF8E1" stopOpacity="0.95" />
          <stop offset="0.2" stopColor="#FFC94D" stopOpacity="0.6" />
          <stop offset="0.55" stopColor="#F0703E" stopOpacity="0.2" />
          <stop offset="1" stopColor="#F0703E" stopOpacity="0" />
        </radialGradient>
      </defs>
      <g clipPath={url("tile")}>
        <rect width="256" height="256" fill={url("sky")} />
        {!small && LOGO_STARS.map(([cx, cy, r, opacity]) => (
          <circle key={`${cx}-${cy}`} cx={cx} cy={cy} r={r} fill="#FFEFB8" opacity={opacity} />
        ))}
        <circle cx="128" cy={planetY} r="232" fill={url("planet")} />
        {!small && (
          <g clipPath={url("planet-clip")} fill="none">
            {LOGO_CONTOURS.map(([r, color, opacity]) => (
              <circle key={r} cx="128" cy={planetY} r={r} stroke={color} strokeWidth="1.5" opacity={opacity} />
            ))}
            {LOGO_GRID.map((x) => (
              <path key={x} d={`M128 138L${x} 256`} stroke="#43306F" strokeWidth="1.2" strokeLinecap="round" opacity="0.5" />
            ))}
          </g>
        )}
        <circle cx="128" cy={planetY} r="232" fill="none" stroke={url("limb")} strokeWidth={small ? 12 : 5} />
        <circle cx="128" cy={flareY} r="120" fill={url("flare")} />
        {small ? (
          <>
            <path d="M4 146 128 136 252 146 128 156Z" fill="#FFF8E1" />
            <path d="M128 40 138 146 128 252 118 146Z" fill="#FFF8E1" />
            <circle cx="128" cy="146" r="16" fill="#FFFFFF" />
          </>
        ) : (
          <>
            <path d="M8 134 128 130.5 248 134 128 137.5Z" fill="#FFF8E1" />
            <path d="M128 70 131 134 128 200 125 134Z" fill="#FFF8E1" />
            <circle cx="128" cy="134" r="7" fill="#FFFFFF" />
          </>
        )}
      </g>
      <rect x="2" y="2" width="252" height="252" rx="50" fill="none" stroke="#FFFFFF" strokeOpacity="0.14" strokeWidth="4" />
    </svg>
  );
}

export const GripIcon = ({ size = 14 }: P) => (
  <svg width={(size * 10) / 14} height={size} viewBox="0 0 10 14" fill="currentColor" aria-hidden>
    <circle cx="3" cy="3" r="1.2" />
    <circle cx="7" cy="3" r="1.2" />
    <circle cx="3" cy="7" r="1.2" />
    <circle cx="7" cy="7" r="1.2" />
    <circle cx="3" cy="11" r="1.2" />
    <circle cx="7" cy="11" r="1.2" />
  </svg>
);

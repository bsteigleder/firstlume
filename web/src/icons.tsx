// Stroke icons shared by the UI. All are decorative: pair them with text or an aria-label.

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

export const ExternalIcon = ({ size = 13 }: P) =>
  svg(size, <path d="M9 2.5h4.5V7M13.5 2.5 7 9M11.5 9.5v3a1 1 0 0 1-1 1h-7a1 1 0 0 1-1-1v-7a1 1 0 0 1 1-1h3" />, "0 0 16 16", 1.5);

export const PeopleIcon = ({ size = 13 }: P) =>
  svg(size, <path d="M1.5 13.5c.6-2.5 2.4-3.75 4.5-3.75s3.9 1.25 4.5 3.75M11 3.5a2.4 2.4 0 0 1 0 4.5M12.5 9.9c1 .5 1.7 1.6 2 3.6M6 8a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5z" />, "0 0 16 16", 1.5);

export function Logo({ size = 22 }: P) {
  return (
    <svg className="logo" width={size} height={size} viewBox="0 0 24 24" aria-hidden>
      <circle cx="12" cy="12" r="4" fill="currentColor" />
      <g stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
        <path d="M12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3M5.3 5.3l2.1 2.1M16.6 16.6l2.1 2.1M5.3 18.7l2.1-2.1M16.6 7.4l2.1-2.1" />
      </g>
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

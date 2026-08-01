/** Minimal 1.6px stroke icon set, sized via `size`, colored via currentColor. */
type IconProps = { size?: number; className?: string };

function base(size: number, className?: string) {
  return {
    width: size,
    height: size,
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: 1.6,
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    className,
    "aria-hidden": true,
  };
}

export const SunIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <circle cx="12" cy="12" r="4" />
    <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
  </svg>
);

export const MoonIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <path d="M21 12.8A8.5 8.5 0 1 1 11.2 3a6.5 6.5 0 0 0 9.8 9.8Z" />
  </svg>
);

export const MonitorIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <rect x="3" y="4" width="18" height="12" rx="1.5" />
    <path d="M9 20h6M12 16v4" />
  </svg>
);

export const PlusIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <path d="M12 5v14M5 12h14" />
  </svg>
);

export const RestartIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <path d="M21 12a9 9 0 1 1-2.6-6.4M21 4v4h-4" />
  </svg>
);

export const TrashIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <path d="M4 7h16M9 7V5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2M6 7l1 13a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1l1-13" />
  </svg>
);

export const CopyIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <rect x="9" y="9" width="12" height="12" rx="2" />
    <path d="M6 15H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v1" />
  </svg>
);

export const CheckIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <path d="M5 12.5l4.5 4.5L19 6.5" />
  </svg>
);

export const SignOutIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    {/* Door on the left, arrow leaving to the right. The previous drawing ran
        the shaft straight through the door panel, so at 16px it read as a
        smudge rather than as anything leaving anywhere. */}
    <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9" />
  </svg>
);

export const ArrowUpIcon = ({ size = 14, className }: IconProps) => (
  <svg {...base(size, className)}>
    <path d="M12 19V6M6 11l6-6 6 6" />
  </svg>
);

export const ArrowDownIcon = ({ size = 14, className }: IconProps) => (
  <svg {...base(size, className)}>
    <path d="M12 5v13M6 12l6 6 6-6" />
  </svg>
);

export const SlidersIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <path d="M4 6h10M18 6h2M4 12h4M12 12h8M4 18h10M18 18h2" />
    <circle cx="16" cy="6" r="2" />
    <circle cx="10" cy="12" r="2" />
    <circle cx="16" cy="18" r="2" />
  </svg>
);

export const LinkIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.5 1.5" />
    <path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.5-1.5" />
  </svg>
);

export const PencilIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <path d="M12 20h9" />
    <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
  </svg>
);

export const KeyIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <circle cx="8" cy="15" r="4" />
    <path d="M10.8 12.2 20 3l1.5 1.5-1.5 1.5 1.5 1.5-2.5 2.5-1.5-1.5-2.2 2.2" />
  </svg>
);

export const ShieldIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <path d="M12 3l7 3v5.5c0 4.3-2.9 7.8-7 9.5-4.1-1.7-7-5.2-7-9.5V6Z" />
  </svg>
);

export const MenuIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <path d="M3.5 6.5h17M3.5 12h17M3.5 17.5h17" />
  </svg>
);

/** Points left; `flip` turns it round for the opposite affordance. */
export const ChevronIcon = ({
  size = 16,
  className,
  flip,
}: IconProps & { flip?: boolean }) => (
  <svg {...base(size, className)} style={flip ? undefined : { transform: "scaleX(-1)" }}>
    <path d="M14.5 5.5 8 12l6.5 6.5" />
  </svg>
);

export const NodeIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <rect x="3.5" y="4.5" width="17" height="6" rx="2" />
    <rect x="3.5" y="13.5" width="17" height="6" rx="2" />
    <path d="M7 7.5h.01M7 16.5h.01" />
  </svg>
);

export const CloudIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <path d="M7 18.5a4 4 0 0 1-.4-7.98 5.5 5.5 0 0 1 10.65-1.4A3.75 3.75 0 0 1 17.5 18.5z" />
  </svg>
);

export const StackIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <path d="m12 3.5 8.5 4.25L12 12 3.5 7.75z" />
    <path d="m3.5 12 8.5 4.25L20.5 12" />
    <path d="m3.5 16.25 8.5 4.25 8.5-4.25" />
  </svg>
);

export const UsersIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <circle cx="9" cy="8" r="3.25" />
    <path d="M3.5 19.5a5.5 5.5 0 0 1 11 0" />
    <path d="M16 5.2a3.25 3.25 0 0 1 0 5.6M17 14.4a5.5 5.5 0 0 1 3.5 5.1" />
  </svg>
);

export const RouteIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <circle cx="6" cy="18" r="2.5" />
    <circle cx="18" cy="6" r="2.5" />
    <path d="M15.5 6H10a3.5 3.5 0 0 0 0 7h4a3.5 3.5 0 0 1 0 7H8.5" />
  </svg>
);

export const AlertIcon = ({ size = 16, className }: IconProps) => (
  <svg {...base(size, className)}>
    <path d="M12 4.5a5.5 5.5 0 0 0-5.5 5.5c0 4-1.5 5.5-1.5 5.5h14s-1.5-1.5-1.5-5.5A5.5 5.5 0 0 0 12 4.5z" />
    <path d="M10.5 19a1.8 1.8 0 0 0 3 0" />
  </svg>
);

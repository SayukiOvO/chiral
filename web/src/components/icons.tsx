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
    <path d="M15 4h3a1 1 0 0 1 1 1v14a1 1 0 0 1-1 1h-3M10 12h9M16 9l3 3-3 3" />
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

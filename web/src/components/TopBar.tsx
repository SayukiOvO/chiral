import { Mark } from "./Mark";
import { ThemeToggle } from "./ThemeToggle";
import { IconButton } from "./ui";
import { SignOutIcon } from "./icons";

export function TopBar({ onSignOut }: { onSignOut: () => void }) {
  return (
    <header className="sticky top-0 z-30 border-b border-line bg-paper/80 backdrop-blur-md">
      <div className="mx-auto flex h-14 max-w-[1120px] items-center justify-between px-5 sm:px-8">
        <div className="flex items-center gap-2.5">
          <span className="text-signal">
            <Mark size={22} />
          </span>
          <span className="font-display text-[17px] font-semibold tracking-tight">Chiral</span>
        </div>
        <div className="flex items-center gap-2">
          <ThemeToggle />
          <IconButton label="退出" onClick={onSignOut}>
            <SignOutIcon size={16} />
          </IconButton>
        </div>
      </div>
    </header>
  );
}

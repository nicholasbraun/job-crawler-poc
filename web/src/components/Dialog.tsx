import { useRef } from "react";
import type { ReactNode } from "react";

import { Icon } from "./primitives";

// Dialog is the shared modal scaffold: a backdrop that closes on outside click,
// a form container that traps Tab focus and closes on Escape, and a title row
// with a close button. Consumers render their fields + a .dialog-actions row as
// children and submit via onSubmit. The discovery-start and import modals share
// this one backdrop/focus-trap/Escape implementation.
export function Dialog({
  title,
  onClose,
  onSubmit,
  children,
}: {
  title: string;
  onClose: () => void;
  onSubmit: (e: React.FormEvent) => void;
  children: ReactNode;
}) {
  const ref = useRef<HTMLFormElement>(null);

  // Escape closes the dialog; Tab cycles focus within it so keyboard focus never
  // slips to the page behind the backdrop.
  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") {
      e.preventDefault();
      onClose();
      return;
    }
    if (e.key !== "Tab") return;
    const focusables = ref.current?.querySelectorAll<HTMLElement>(
      'button:not([disabled]), input, textarea, [href], [tabindex]:not([tabindex="-1"])',
    );
    if (!focusables || focusables.length === 0) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  };

  return (
    <div className="dialog-backdrop" onClick={onClose}>
      <form
        ref={ref}
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onClick={(e) => e.stopPropagation()}
        onSubmit={onSubmit}
        onKeyDown={onKeyDown}
      >
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
          <div className="dialog-title">{title}</div>
          <button type="button" className="btn btn-icon btn-secondary" onClick={onClose} aria-label="Close">
            <Icon name="ph-x" size={16} />
          </button>
        </div>
        {children}
      </form>
    </div>
  );
}

"use client";

import { useState } from "react";
import { CopyIcon, CheckIcon } from "./icons";
import styles from "./CopyButton.module.css";

export default function CopyButton({
  value,
  label = "Copy command",
}: {
  value: string;
  label?: string;
}) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1800);
    } catch {
      /* clipboard unavailable — no-op */
    }
  }

  return (
    <button
      type="button"
      className={styles.button}
      onClick={copy}
      aria-label={copied ? "Copied" : label}
    >
      {copied ? (
        <CheckIcon width={16} height={16} style={{ color: "var(--ok)" }} />
      ) : (
        <CopyIcon width={16} height={16} />
      )}
      <span aria-live="polite">{copied ? "Copied" : "Copy"}</span>
    </button>
  );
}

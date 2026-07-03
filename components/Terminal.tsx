import styles from "./Terminal.module.css";

export type Line =
  | { k: "cmd"; t: string }
  | { k: "out"; t: string }
  | { k: "ok"; t: string }
  | { k: "warn"; t: string }
  | { k: "comment"; t: string }
  | { k: "blank" };

export default function Terminal({
  title = "bash",
  lines,
  className,
}: {
  title?: string;
  lines: Line[];
  className?: string;
}) {
  return (
    <div className={`${styles.term} ${className ?? ""}`}>
      <div className={styles.bar}>
        <span className={styles.dots} aria-hidden>
          <i />
          <i />
          <i />
        </span>
        <span className={styles.title}>{title}</span>
      </div>
      <pre className={styles.body} tabIndex={0}>
        {lines.map((line, i) => {
          if (line.k === "blank") return <span key={i}>{"\n"}</span>;
          if (line.k === "cmd") {
            return (
              <span key={i} className={styles.cmd}>
                <span className={styles.prompt}>$ </span>
                {line.t}
                {"\n"}
              </span>
            );
          }
          return (
            <span key={i} className={styles[line.k]}>
              {line.t}
              {"\n"}
            </span>
          );
        })}
      </pre>
    </div>
  );
}

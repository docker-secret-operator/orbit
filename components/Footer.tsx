import { site } from "@/lib/site";
import { OrbitMark } from "./icons";
import styles from "./Footer.module.css";

const links = [
  { label: "Docs", href: site.docs },
  { label: "GitHub", href: site.repo },
  { label: "Releases", href: site.releases },
  { label: "License", href: site.license },
];

export default function Footer() {
  return (
    <footer className={styles.footer}>
      <div className={`container ${styles.inner}`}>
        <a href="#top" className={styles.brand} aria-label="Orbit — home">
          <OrbitMark />
          <span>Orbit</span>
        </a>
        <nav className={styles.links} aria-label="Footer">
          {links.map((l) => (
            <a
              key={l.label}
              href={l.href}
              target="_blank"
              rel="noreferrer"
              className={styles.link}
            >
              {l.label}
            </a>
          ))}
        </nav>
        <p className={styles.note}>MIT licensed · zero-downtime for Compose</p>
      </div>
    </footer>
  );
}

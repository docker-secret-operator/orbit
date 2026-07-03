import { site } from "@/lib/site";
import { OrbitMark, GitHubIcon } from "./icons";
import styles from "./Nav.module.css";

export default function Nav() {
  return (
    <header className={styles.header}>
      <nav className={`container ${styles.nav}`} aria-label="Primary">
        <a href="#top" className={styles.brand} aria-label="Orbit — home">
          <OrbitMark />
          <span>Orbit</span>
        </a>

        <div className={styles.links}>
          <a href="#how" className={`${styles.link} ${styles.hideSm}`}>
            Product
          </a>
          <a href={site.docs} className={styles.link}>
            Docs
          </a>
          <a
            href={site.repo}
            className={styles.link}
            rel="noreferrer"
            target="_blank"
            aria-label="Orbit on GitHub"
          >
            <GitHubIcon width={17} height={17} />
            <span className={styles.linkText}>GitHub</span>
          </a>
          <a href="#install" className={styles.install}>
            Install
          </a>
        </div>
      </nav>
    </header>
  );
}

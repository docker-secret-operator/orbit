import Nav from "@/components/Nav";
import Hero from "@/components/Hero";
import ProblemTimeline from "@/components/ProblemTimeline";
import Comparison from "@/components/Comparison";
import HowItWorks from "@/components/HowItWorks";
import Features from "@/components/Features";
import Cli from "@/components/Cli";
import FailureBehavior from "@/components/FailureBehavior";
import Architecture from "@/components/Architecture";
import ProductionConsiderations from "@/components/ProductionConsiderations";
import KnownScope from "@/components/KnownScope";
import Install from "@/components/Install";
import DocsCta from "@/components/DocsCta";
import Footer from "@/components/Footer";

export default function Page() {
  return (
    <>
      <span id="top" />
      <Nav />
      <main id="main">
        {/* What it is */}
        <Hero />
        <ProblemTimeline />
        <Comparison />
        <HowItWorks />
        <Features />
        <Cli />
        {/* Why you can trust it in production */}
        <FailureBehavior />
        <Architecture />
        <ProductionConsiderations />
        <KnownScope />
        {/* Act */}
        <Install />
        <DocsCta />
      </main>
      <Footer />
    </>
  );
}

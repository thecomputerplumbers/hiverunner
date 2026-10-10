import Link from "next/link"
import { Container } from "@workspace/ui-shell/components/page-header"
import { PageIntro, Prose } from "@/components/marketing/page-parts"
import product from "@/config/product.json"

export const metadata = {
  title: "Privacy policy",
  description:
    "What HiveWorker collects, what it does not, who it is shared with and how to ask for it.",
}

const COMPANY = "The Computer Plumbers LLC"
const EFFECTIVE = "October 9, 2026"

export default function PrivacyPage() {
  const contact = (
    <a href={`mailto:${product.supportEmail}`}>{product.supportEmail}</a>
  )
  return (
    <>
      <PageIntro title="Privacy policy">Effective {EFFECTIVE}.</PageIntro>
      <Container className="pb-16 sm:pb-24">
        <Prose>
          <p>
            {product.name} is a service of {COMPANY} (&quot;we&quot;,
            &quot;us&quot;). This policy says what personal information the
            service collects, why, and what you can do about it. The{" "}
            <Link href="/security">security page</Link> describes the same
            ground from the technical side.
          </p>

          <h2>What we collect</h2>
          <ul>
            <li>
              <strong>Your account.</strong> When you sign in with GitHub we
              receive your GitHub user ID, username, display name, avatar and
              email address.
            </li>
            <li>
              <strong>Your organization.</strong> For an organization that has
              installed the GitHub App: its name, and for people who sign in,
              whether GitHub says they are members or owners and whether they
              are on a team the hive is limited to.
            </li>
            <li>
              <strong>Machines.</strong> For each machine added to a hive: the
              name it reports, model, operating system, architecture, number of
              cores, memory, a measured speed score, the daemon&apos;s version,
              whether it is connected, and when it was last seen.
            </li>
            <li>
              <strong>Jobs.</strong> For each job: the repository name, the
              workflow run and job IDs, the job&apos;s name and label, its size,
              when it started and ended, its result, the machine that ran it and
              its price.
            </li>
            <li>
              <strong>Payments.</strong> Your organization&apos;s balance and
              its history. Card payments are handled by Stripe; we receive a
              customer reference and the outcome of a payment, not the card
              number.
            </li>
            <li>
              <strong>Connected apps.</strong> If you connect an agent or other
              client, which one it is and what you allowed it to do.
            </li>
            <li>
              <strong>Technical records.</strong> IP address, browser type and
              the time of requests, kept with your sign-in sessions and in
              server logs.
            </li>
            <li>
              <strong>Messages.</strong> What you send when you write to us.
            </li>
          </ul>

          <h2>What we do not collect</h2>
          <p>
            The service does not receive your source code, build output, job
            logs, artifacts, caches or secrets. The runner inside a job&apos;s
            virtual machine talks to GitHub directly, and the GitHub App has no
            permission to read code, repository contents or secrets.
          </p>

          <h2>How we use it</h2>
          <ul>
            <li>to sign you in and decide which hives you belong to;</li>
            <li>to assign jobs to machines and show a hive its activity;</li>
            <li>to price jobs, keep balances and take payment;</li>
            <li>to keep the service secure and prevent abuse;</li>
            <li>
              to send messages about your account, such as a low balance or a
              budget being reached;
            </li>
            <li>to fix problems and improve the service;</li>
            <li>to meet legal obligations.</li>
          </ul>
          <p>
            Where the law asks for a legal basis, we rely on performing our
            agreement with you, our legitimate interest in running and securing
            the service, and legal obligation.
          </p>

          <h2>Cookies</h2>
          <p>
            We set a cookie to keep you signed in, and your browser stores your
            choice of light or dark theme. We do not use advertising cookies or
            third-party analytics.
          </p>

          <h2>Who sees it</h2>
          <ul>
            <li>
              <strong>Other members of your hive.</strong> They can see the
              machines in it, who added each one, the jobs it has run and its
              leaderboard.
            </li>
            <li>
              <strong>Providers that run the service for us.</strong> GitHub,
              for sign-in and to run jobs; Cloudflare, for hosting, storage and
              email; Stripe, for payments. Each handles information under its
              own terms and only to provide its service.
            </li>
            <li>
              <strong>Authorities,</strong> when the law requires it or to
              protect rights and safety.
            </li>
            <li>
              <strong>A successor,</strong> if the service is sold or merged,
              under this policy.
            </li>
          </ul>
          <p>We do not sell personal information.</p>

          <h2>How long we keep it</h2>
          <p>
            We keep account, machine and job records while your organization
            uses the service. Payment and balance records are kept as long as
            accounting and tax rules require. Sign-in sessions expire, and
            server logs are kept for a limited time. When you ask us to delete
            your information we do so, except what we must keep by law.
          </p>

          <h2>Security</h2>
          <p>
            We protect information with access controls and encryption in
            transit. No service can promise perfect security. If we learn of a
            breach that affects you, we will tell you as the law requires.
          </p>

          <h2>Your choices and rights</h2>
          <p>
            You can remove a machine, uninstall the daemon, or have an owner
            uninstall the GitHub App at any time. Depending on where you live,
            you may have the right to see, correct, export or delete your
            personal information, to object to or restrict how we use it, and to
            complain to your data protection authority. Write to {contact} and
            we will answer within the time the law sets. We will not treat you
            differently for using these rights.
          </p>

          <h2>Where it is processed</h2>
          <p>
            Our providers process information in the United States and in other
            countries where they operate. Where the law requires safeguards for
            such transfers, we rely on the ones our providers offer.
          </p>

          <h2>Children</h2>
          <p>
            The service is for people who work on software. It is not directed
            to children under 16, and we do not knowingly collect their
            information.
          </p>

          <h2>Changes</h2>
          <p>
            We may update this policy. The date at the top shows when it last
            changed, and we will give notice of material changes on this site or
            by email.
          </p>

          <h2>Contact</h2>
          <p>Questions or requests: {contact}</p>
        </Prose>
      </Container>
    </>
  )
}

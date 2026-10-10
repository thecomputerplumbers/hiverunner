import Link from "next/link"
import { Container } from "@workspace/ui-shell/components/page-header"
import { PageIntro, Prose } from "@/components/marketing/page-parts"
import product from "@/config/product.json"

export const metadata = {
  title: "Terms of service",
  description:
    "The terms that apply to using HiveWorker: accounts, machines, jobs, payment and liability.",
}

const COMPANY = "The Computer Plumbers"
const EFFECTIVE = "October 9, 2026"

export default function TermsPage() {
  const contact = (
    <a href={`sms:${product.supportSms}`}>{product.supportSmsDisplay}</a>
  )
  return (
    <>
      <PageIntro title="Terms of service">Effective {EFFECTIVE}.</PageIntro>
      <Container className="pb-16 sm:pb-24">
        <Prose>
          <h2>1. These terms</h2>
          <p>
            {product.name} is a service of {COMPANY} (&quot;we&quot;,
            &quot;us&quot;). These terms are an agreement between you and us. By
            installing the {product.name} GitHub App, signing in, adding a
            machine or otherwise using the service, you accept them. If you use
            the service for an organization, you accept them for that
            organization and confirm you are allowed to.
          </p>

          <h2>2. The service</h2>
          <p>
            {product.name} assigns GitHub Actions jobs from a GitHub
            organization to machines that members of that organization have
            added, and keeps account of what each job cost. Each job runs under
            GitHub&apos;s own runner. Your use of GitHub, including GitHub
            Actions, remains subject to your agreement with GitHub, and the
            service depends on GitHub being available.
          </p>

          <h2>3. Accounts and organizations</h2>
          <p>
            You sign in with GitHub. Who belongs to a hive follows who belongs
            to the GitHub organization, and the organization&apos;s owners
            decide which members may add machines. You are responsible for
            activity under your account and, as an owner, for the settings you
            choose for your organization.
          </p>

          <h2>4. Machines you add</h2>
          <p>
            Add only machines you own or are authorized to use this way,
            including under your employer&apos;s policies. A machine you add
            will run jobs from the organizations it serves. Jobs run in a
            virtual machine that is destroyed when the job ends, except macOS
            jobs, which run on the Mac itself as a separate user and only when
            both the machine&apos;s owner and the hive have turned them on. You
            can pause the daemon or uninstall it at any time. The{" "}
            <Link href="/security">security page</Link> describes how isolation
            works; it is a description, not a guarantee.
          </p>

          <h2>5. Acceptable use</h2>
          <p>You agree not to use the service to:</p>
          <ul>
            <li>break the law or infringe anyone&apos;s rights;</li>
            <li>
              run workloads that are not continuous integration or related build
              and test work, such as cryptocurrency mining;
            </li>
            <li>
              attack, probe or interfere with the service, a machine in a hive,
              or anyone else&apos;s systems;
            </li>
            <li>
              attempt to escape a job&apos;s virtual machine or reach a host
              machine&apos;s files, credentials or network beyond what the job
              is given;
            </li>
            <li>avoid, falsify or interfere with metering or payment;</li>
            <li>violate GitHub&apos;s terms.</li>
          </ul>

          <h2>6. Prices, credit and payment</h2>
          <p>
            Jobs are charged by the minute at the prices on the{" "}
            <Link href="/pricing">pricing page</Link>, against your
            organization&apos;s balance. A new organization receives trial
            credit. Trial and promotional credit has no cash value and cannot be
            transferred or redeemed. You add to the balance by card; card
            payments are processed by Stripe, and we do not store card numbers.
            Purchased credit is not refundable except where the law requires it.
            Prices exclude taxes, which you are responsible for. We may change
            prices by updating the pricing page; a change applies to jobs that
            start after it.
          </p>

          <h2>7. Credit for work a machine does</h2>
          <p>
            When a machine runs a job, a share of that job&apos;s price is
            credited to the organization or to the developer whose machine it
            is, as the hive&apos;s owners have chosen. We do not promise any
            amount of work or of credit, and a hive&apos;s owners may change
            that choice.
          </p>

          <h2>8. Your code and data</h2>
          <p>
            You keep all rights in your code and everything your jobs produce.
            The service does not receive your source code, build output, job
            logs, artifacts, caches or secrets; those pass between the runner
            and GitHub. What we do hold is described in the{" "}
            <Link href="/privacy">privacy policy</Link>.
          </p>

          <h2>9. Software</h2>
          <p>
            The {product.name} software is published under the GNU Affero
            General Public License, version 3, and that license governs your use
            of its code. These terms govern your use of the service we operate.
            The {product.name} name and marks are ours.
          </p>

          <h2>10. Other providers</h2>
          <p>
            The service relies on GitHub, on Cloudflare for hosting, and on
            Stripe for payments. Your use of them is subject to their terms, and
            we are not responsible for their services.
          </p>

          <h2>11. Availability and changes</h2>
          <p>
            We aim to keep the service running but do not promise that it will
            be uninterrupted or free of errors, and it comes with no service
            level commitment. We may change, limit or stop any part of it. If we
            stop the service altogether we will say so on this site in advance
            where we reasonably can.
          </p>

          <h2>12. Ending use</h2>
          <p>
            You can stop at any time by uninstalling the GitHub App and the
            daemon. We may suspend or end access for an account or organization
            that breaks these terms, puts the service or others at risk, or
            where the law requires it. Sections that by their nature should
            continue after use ends, including 6, 13, 14 and 15, do continue.
          </p>

          <h2>13. No warranties</h2>
          <p>
            To the extent the law allows, the service is provided &quot;as
            is&quot; and &quot;as available&quot;, without warranties of any
            kind, express or implied, including of merchantability, fitness for
            a particular purpose and non-infringement. You are responsible for
            deciding whether a job is suitable to run on a given machine.
          </p>

          <h2>14. Limit of liability</h2>
          <p>
            To the extent the law allows, we are not liable for indirect,
            incidental, special, consequential or punitive damages, or for lost
            profits, revenue, data or goodwill. Our total liability for all
            claims relating to the service is limited to the greater of the
            amount your organization paid us in the twelve months before the
            claim arose and one hundred US dollars. Nothing in these terms
            limits liability that cannot be limited by law.
          </p>

          <h2>15. Indemnity</h2>
          <p>
            You will defend and indemnify us against claims by others that arise
            from your jobs, the machines you add, or your breach of these terms,
            including reasonable legal costs.
          </p>

          <h2>16. Governing law</h2>
          <p>
            These terms are governed by the laws of the State of Utah, without
            regard to its conflict of laws rules, and the state and federal
            courts located in Utah have exclusive jurisdiction over disputes
            about them, except where the law of your own place gives you rights
            that cannot be waived.
          </p>

          <h2>17. Changes to these terms</h2>
          <p>
            We may update these terms. The date at the top shows when they last
            changed. If a change is material we will give notice on this site or
            by email before it takes effect. Continuing to use the service after
            a change means you accept it.
          </p>

          <h2>18. Contact</h2>
          <p>Questions about these terms: text {contact}</p>
        </Prose>
      </Container>
    </>
  )
}

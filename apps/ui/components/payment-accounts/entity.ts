/**
 * The secrets `entity_type` for a payment account (#5506).
 *
 * Lives in its own module so a server component, a client component and the
 * section can all name it without importing the section (which pulls the vault
 * UI into the bundle). The value is a plain string column
 * (secrets.entity_type is VARCHAR(50), 026), so no migration is needed to
 * introduce it — and naming the account here is what makes a bank login list
 * under the account as well as behind it.
 */
export const PAYMENT_ACCOUNT_ENTITY = "payment_account";

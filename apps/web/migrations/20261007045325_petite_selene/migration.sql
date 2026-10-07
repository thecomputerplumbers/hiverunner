CREATE TABLE `auto_top_up` (
	`organization_id` text PRIMARY KEY,
	`enabled` integer DEFAULT false NOT NULL,
	`amount_cents` integer NOT NULL,
	`payment_method_id` text,
	`card_brand` text,
	`card_last4` text,
	`pending_since` integer,
	`last_error` text,
	`updated_at` integer NOT NULL,
	CONSTRAINT `fk_auto_top_up_organization_id_organization_id_fk` FOREIGN KEY (`organization_id`) REFERENCES `organization`(`id`) ON DELETE CASCADE
);

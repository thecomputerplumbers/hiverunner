ALTER TABLE `auto_top_up` ADD `below_cents` integer DEFAULT 500 NOT NULL;--> statement-breakpoint
ALTER TABLE `auto_top_up` ADD `monthly_cap_cents` integer;
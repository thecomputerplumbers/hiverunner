CREATE TABLE `runner_scale_set` (
	`organization_id` text NOT NULL,
	`label` text NOT NULL,
	`installation_id` integer NOT NULL,
	`scale_set_id` integer NOT NULL,
	`runner_group_id` integer NOT NULL,
	`updated_at` integer NOT NULL,
	CONSTRAINT `runner_scale_set_pk` PRIMARY KEY(`organization_id`, `label`),
	CONSTRAINT `fk_runner_scale_set_organization_id_organization_id_fk` FOREIGN KEY (`organization_id`) REFERENCES `organization`(`id`) ON DELETE CASCADE
);

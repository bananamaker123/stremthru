-- +goose Up
-- +goose StatementBegin
ALTER TABLE `nzb_info` ADD COLUMN `date` datetime;
ALTER TABLE `nzb_info` ADD COLUMN `status` varchar NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE `nzb_info` DROP COLUMN `status`;
ALTER TABLE `nzb_info` DROP COLUMN `date`;
-- +goose StatementEnd

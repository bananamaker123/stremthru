-- +goose Up
-- +goose StatementBegin
ALTER TABLE "public"."nzb_info" ADD COLUMN "date" timestamptz;
ALTER TABLE "public"."nzb_info" ADD COLUMN "status" text NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE "public"."nzb_info" DROP COLUMN IF EXISTS "status";
ALTER TABLE "public"."nzb_info" DROP COLUMN IF EXISTS "date";
-- +goose StatementEnd

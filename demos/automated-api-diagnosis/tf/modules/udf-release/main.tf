# Names the release that holds the EmitAfterGap jar, from the function's source alone.
#
# The tag is the first 16 hex digits of a hash of pom.xml, build.sh (which pins the build
# image) and src/main/**, so it changes exactly when the jar would. Nothing has to be committed to say which jar a commit wants:
# the demo's own source says it. The release workflow (.github/workflows/
# automated-api-diagnosis-udf.yml) runs this same module to name the release it creates, so
# Terraform and the workflow cannot disagree about the tag. Tests are left out of the
# hash: changing them does not change the jar.
#
# It has no providers, so the workflow can run it without downloading any.

variable "source_dir" {
  description = "The Maven project of the function (udf/emit-after-gap)."
  type        = string
}

locals {
  # fileset returns paths relative to source_dir, and sort makes the order fixed.
  files = sort(concat(["pom.xml", "build.sh"], tolist(fileset(var.source_dir, "src/main/**"))))

  source_hash = sha256(join("", [for f in local.files : "${f}:${filesha256("${var.source_dir}/${f}")}\n"]))
}

output "tag" {
  description = "The release tag, emit-after-gap-<hash>."
  value       = "emit-after-gap-${substr(local.source_hash, 0, 16)}"
}

## IAM resources

import {
  id = "projects/${var.project}/serviceAccounts/orchestrator@${var.project}.iam.gserviceaccount.com"
  to = google_service_account.orchestrator
}
import {
  id = "projects/${var.project}/serviceAccounts/builder-remote@${var.project}.iam.gserviceaccount.com"
  to = google_service_account.builder-remote
}
import {
  id = "projects/${var.project}/serviceAccounts/builder-local@${var.project}.iam.gserviceaccount.com"
  to = google_service_account.builder-local
}
import {
  id = "projects/${var.project}/serviceAccounts/inference@${var.project}.iam.gserviceaccount.com"
  to = google_service_account.inference
}
import {
  id = "projects/${var.project}/serviceAccounts/git-cache@${var.project}.iam.gserviceaccount.com"
  to = google_service_account.git-cache
}
import {
  id = "projects/${var.project}/serviceAccounts/gateway@${var.project}.iam.gserviceaccount.com"
  to = google_service_account.gateway
}
import {
  id = "projects/${var.project}/serviceAccounts/attestation-pubsub-reader@${var.project}.iam.gserviceaccount.com"
  to = google_service_account.attestation-pubsub-reader
}

## KMS resources

import {
  id = "${var.project}/cloudkms.googleapis.com"
  to = google_project_service.cloudkms
}
import {
  id = "projects/${var.project}/locations/global/keyRings/ring"
  to = google_kms_key_ring.ring
}
import {
  id = "projects/${var.project}/locations/global/keyRings/ring/cryptoKeys/signing-key"
  to = google_kms_crypto_key.signing-key
}

## Firestore resources

import {
  id = "${var.project}/compute.googleapis.com"
  to = google_project_service.compute
}
import {
  id = "${var.project}/appengine.googleapis.com"
  to = google_project_service.gae
}
import {
  id = "${var.project}"
  to = google_app_engine_application.dummy_app
}

import {
  id = "${var.project}/firestore.googleapis.com"
  to = google_project_service.firestore
}

## Storage resources

import {
  id = "${var.project}/storage.googleapis.com"
  to = google_project_service.storage
}
import {
  id = "${var.host}-rebuild-attestations"
  to = google_storage_bucket.attestations
}
import {
  id = "${var.host}-rebuild-metadata"
  to = google_storage_bucket.metadata
}
import {
  id = "${var.host}-rebuild-logs"
  to = google_storage_bucket.logs
}
import {
  id = "${var.host}-rebuild-debug"
  to = google_storage_bucket.debug
}
import {
  id = "${var.host}-rebuild-git-cache"
  to = google_storage_bucket.git-cache
}
import {
  id = "${var.host}-rebuild-bootstrap-tools"
  to = google_storage_bucket.bootstrap-tools
}

## PubSub resources

import {
  id = "projects/${var.project}/topics/oss-rebuild-attestation-topic"
  to = google_pubsub_topic.attestation-topic
}
import {
  id = "${google_storage_bucket.attestations.name}/projects/_/buckets/${google_storage_bucket.attestations.name}/notificationConfigs/${google_storage_notification.attestation-notification.id}"
  to = google_storage_notification.attestation-notification
}
import {
  id = "projects/${var.project}/locations/us-central1/repositories/service-images"
  to = google_artifact_registry_repository.registry
}

## Compute resources

import {
  id = "${var.project}/cloudbuild.googleapis.com"
  to = google_project_service.cloudbuild
}
import {
  id = "${var.project}/run.googleapis.com"
  to = google_project_service.run
}
import {
  id = "projects/${var.project}/locations/us-central1/services/gateway"
  to = google_cloud_run_v2_service.gateway
}
import {
  id = "projects/${var.project}/locations/us-central1/services/git-cache"
  to = google_cloud_run_v2_service.git-cache
}
import {
  id = "projects/${var.project}/locations/us-central1/services/build-local"
  to = google_cloud_run_v2_service.build-local
}
import {
  id = "projects/${var.project}/locations/us-central1/services/inference"
  to = google_cloud_run_v2_service.inference
}
import {
  id = "projects/${var.project}/locations/us-central1/services/api"
  to = google_cloud_run_v2_service.orchestrator
}

## IAM Bindings

import {
  id = "projects/${var.project}/roles/bucketViewer"
  to = google_project_iam_custom_role.bucket-viewer-role
}
import {
  id = "${google_storage_bucket.git-cache.name} roles/storage.objectAdmin serviceAccount:${google_service_account.git-cache.email}"
  to = google_storage_bucket_iam_binding.git-cache-manages-git-cache
}
import {
  id = "${google_storage_bucket.git-cache.name} roles/storage.objectViewer serviceAccount:${google_service_account.builder-local.email}"
  to = google_storage_bucket_iam_binding.local-build-reads-git-cache
}
import {
  id = "${google_storage_bucket.attestations.name} roles/storage.objectCreator serviceAccount:${google_service_account.orchestrator.email}"
  to = google_storage_bucket_iam_binding.orchestrator-writes-attestations
}
import {
  id = "${google_storage_bucket.metadata.name} roles/storage.objectAdmin serviceAccount:${google_service_account.orchestrator.email}"
  to = google_storage_bucket_iam_binding.orchestrator-manages-metadata
}
import {
  id = "${google_storage_bucket.debug.name} roles/storage.objectCreator serviceAccount:${google_service_account.orchestrator.email}"
  to = google_storage_bucket_iam_binding.orchestrator-and-local-build-write-debug
}
import {
  id = "${google_storage_bucket.metadata.name} roles/storage.objectCreator serviceAccount:${google_service_account.builder-remote.email}"
  to = google_storage_bucket_iam_binding.remote-build-writes-metadata
}
import {
  id = "projects/${var.project} roles/datastore.user serviceAccount:${google_service_account.orchestrator.email}"
  to = google_project_iam_binding.orchestrator-uses-datastore
}
import {
  id = "projects/${var.project}/locations/us-central1/services/${google_cloud_run_v2_service.build-local.name} roles/run.invoker serviceAccount:${google_service_account.orchestrator.email}"
  to = google_cloud_run_v2_service_iam_binding.orchestrator-calls-build-local
}
import {
  id = "projects/${var.project}/locations/global/keyRings/ring/cryptoKeys/signing-key roles/cloudkms.verifier allUsers"
  to = google_kms_crypto_key_iam_binding.signing-key-is-public
}
import {
  id = "${google_storage_bucket.attestations.name} roles/storage.objectViewer allUsers"
  to = google_storage_bucket_iam_binding.attestation-bucket-is-public
}
import {
  id = "${google_storage_bucket.bootstrap-tools.name} roles/storage.objectViewer allUsers"
  to = google_storage_bucket_iam_binding.bootstrap-bucket-is-public
}

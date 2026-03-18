import type { ResultPromise } from 'execa'

import type { StartedTestContainer } from 'testcontainers'
import fs from 'node:fs/promises'
import path from 'node:path'
import { PostgreSqlContainer } from '@testcontainers/postgresql'

import { configDotenv } from 'dotenv'
import { execa } from 'execa'

import { GenericContainer } from 'testcontainers'
import waitOn from 'wait-on'

type DBDriver = 'sqlite' | 'postgres'
type StorageDriver = 'filesystem' | 'gcs'

let server: ResultPromise
const testContainers: (StartedTestContainer | undefined)[] = []

export async function setup() {
  const dbDriver = (process.env.VITEST_DB_DRIVER ?? 'sqlite') as DBDriver
  const storageDriver = (process.env.VITEST_STORAGE_DRIVER ?? 'filesystem') as StorageDriver

  const result = configDotenv({
    path: [`tests/.env.base`],
  })
  if (result.error) throw result.error

  await fs.rm('tests/temp', { force: true, recursive: true })

  // Set env vars required by @actions/cache
  process.env.GITHUB_WORKSPACE = process.cwd()
  process.env.RUNNER_TEMP = path.join(process.cwd(), 'tests/temp')
  process.env.ACTIONS_RUNTIME_TOKEN = process.env.ACTIONS_RUNTIME_TOKEN || 'mock-runtime-token'

  console.debug('Starting test containers for', dbDriver, storageDriver)

  // DB container
  if (dbDriver === 'postgres') {
    const container = await new PostgreSqlContainer('postgres:latest')
      .withDatabase('postgres')
      .withPassword('postgres')
      .withUsername('postgres')
      .withExposedPorts({ host: 5432, container: 5432 })
      .start()
    testContainers.push(container)
    process.env.DATABASE_URL =
      'postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable'
  }

  // Storage container
  if (storageDriver === 'gcs') {
    const container = await new GenericContainer('fsouza/fake-gcs-server:latest')
      .withEntrypoint(['sh'])
      .withCommand([
        `-c`,
        `mkdir -p /data/test && /bin/fake-gcs-server -scheme http -port 9000 -data /data`,
      ])
      .withExposedPorts({ container: 9000, host: 9000 })
      .withHealthCheck({
        test: ['CMD-SHELL', 'curl --fail http://localhost:9000/storage/v1/b'],
        interval: 1000,
        retries: 30,
        startPeriod: 1000,
      })
      .start()
    testContainers.push(container)
    process.env.STORAGE_GCS_BUCKET = 'test'
    process.env.STORAGE_GCS_ENDPOINT = 'http://localhost:9000/storage/v1/'
  }

  // Build Go binary
  console.debug('Building Go server...')
  await execa('go', ['build', '-o', '.output/cache-server', './cmd/server/'], {
    stdio: 'inherit',
  })

  // Set env vars for the Go server
  const env: Record<string, string> = {
    ...(process.env as Record<string, string>),
    API_BASE_URL: process.env.API_BASE_URL ?? 'http://localhost:3000',
    STORAGE_DRIVER: storageDriver,
    DB_DRIVER: dbDriver,
    PORT: '3000',
  }

  if (storageDriver === 'filesystem') {
    env.STORAGE_FILESYSTEM_PATH = '.data/test-storage'
  }

  if (dbDriver === 'sqlite') {
    env.SQLITE_PATH = '.data/test-cache.db'
  }

  // Start Go server
  console.debug('Starting Go server...')
  server = execa('.output/cache-server', [], {
    stdio: 'inherit',
    env,
  })

  server.on('exit', (code) => {
    if (code === 0 || code === null) return
    throw new Error(`Go server exited with code ${code}`)
  })
  server.addListener('error', (err) => {
    throw err
  })

  // Wait for server to be ready
  await waitOn({
    resources: ['http://localhost:3000/healthz'],
    timeout: 30_000,
    interval: 100,
  })

  console.debug('Go server ready')
}

export async function teardown() {
  await fs.rm('tests/temp', { recursive: true, force: true })
  await fs.rm('.data/test-storage', { recursive: true, force: true })
  await fs.rm('.data/test-cache.db', { force: true })
  await fs.rm('.data/test-cache.db-wal', { force: true })
  await fs.rm('.data/test-cache.db-shm', { force: true })
  server?.kill()
  await Promise.all(testContainers.map((container) => container?.stop()))
}

import os
from dataclasses import dataclass


@dataclass
class DatabaseConfig:
    host: str
    port: int
    user: str
    password: str
    db_name: str
    ssl_mode: bool
    ssl_cert_path: str = None
    pool_size: int = 10
    pool_timeout: int = 30
    max_retries: int = 5
    retry_delay: int = 5

    @staticmethod
    def from_env() -> 'DatabaseConfig':
        return DatabaseConfig(
            host=os.getenv('DB_HOST', 'localhost'),
            port=int(os.getenv('DB_PORT', '5432')),
            user=os.getenv('DB_USER', 'user'),
            password=os.getenv('DB_PASSWORD', 'password'),
            db_name=os.getenv('DB_NAME', 'media_db'),
            ssl_mode=os.getenv('DB_SSL_MODE', 'false').lower() == 'true',
            ssl_cert_path=os.getenv('DB_SSL_CERT_PATH'),
            pool_size=int(os.getenv('DB_POOL_SIZE', '10')),
            pool_timeout=int(os.getenv('DB_POOL_TIMEOUT', '30')),
            max_retries=int(os.getenv('DB_MAX_RETRIES', '5')),
            retry_delay=int(os.getenv('DB_RETRY_DELAY', '5')),
        )

@dataclass
class RedisConfig:
    connection_string: str
    pool_size: int = 10
    pool_timeout: int = 30
    max_retries: int = 5
    retry_delay: int = 5
    connect_timeout: int = 10
    read_timeout: int = 10
    write_timeout: int = 10

    @staticmethod
    def from_env() -> 'RedisConfig':
        return RedisConfig(
            connection_string=os.getenv('REDIS_CONNECTION_STRING', 'redis://localhost:6379/0'),
            pool_size=int(os.getenv('REDIS_POOL_SIZE', '10')),
            pool_timeout=int(os.getenv('REDIS_POOL_TIMEOUT', '30')),
            max_retries=int(os.getenv('REDIS_MAX_RETRIES', '5')),
            retry_delay=int(os.getenv('REDIS_RETRY_DELAY', '5')),
            connect_timeout=int(os.getenv('REDIS_CONNECT_TIMEOUT', '10')),
            read_timeout=int(os.getenv('REDIS_READ_TIMEOUT', '10')),
            write_timeout=int(os.getenv('REDIS_WRITE_TIMEOUT', '10')),
        )

@dataclass
class BucketConfig:
    bucket_name: str
    region: str
    access_key: str
    secret_key: str
    endpoint_url: str = None
    provider: str = 'gcs'
    sa_path: str = None  # Service Account Path for GCS

    @staticmethod
    def from_env() -> 'BucketConfig':
        return BucketConfig(
            provider=os.getenv('BUCKET_PROVIDER', 'gcs'),
            bucket_name=os.getenv('BUCKET_NAME', 'media-bucket'),
            region=os.getenv('BUCKET_REGION', 'us-east-1'),
            access_key=os.getenv('BUCKET_ACCESS_KEY', 'access_key'),
            secret_key=os.getenv('BUCKET_SECRET_KEY', 'secret_key'),
            endpoint_url=os.getenv('BUCKET_ENDPOINT_URL'),
            sa_path=os.getenv('BUCKET_SA_PATH', '.secrets/aion-staging-d4d9b9c808ec.json'),
        )

@dataclass
class WorkerConfig:
    database: DatabaseConfig
    redis: RedisConfig
    bucket: BucketConfig
    temp_dir: str
    stream_name: str
    worker_id: str
    consumer_group: str = 'worker-group'
    max_concurrent_jobs: int = 5
    job_poll_interval: int = 10

    @staticmethod
    def from_env() -> 'WorkerConfig':
        # get OS based temp dir if available
        curr_os = os.name
        if curr_os == 'nt':
            temp_dir = os.getenv('TEMP', 'C:\\Temp\\worker')
        else:
            temp_dir = os.getenv('TMPDIR', '/tmp/worker')

        return WorkerConfig(
            database=DatabaseConfig.from_env(),
            redis=RedisConfig.from_env(),
            bucket=BucketConfig.from_env(),
            worker_id=os.getenv('WORKER_ID', 'worker-1'),
            max_concurrent_jobs=int(os.getenv('MAX_CONCURRENT_JOBS', '5')),
            job_poll_interval=int(os.getenv('JOB_POLL_INTERVAL', '10')),
            temp_dir=temp_dir,
            stream_name=os.getenv('STREAM_NAME', 'media:jobs'),
            consumer_group=os.getenv('CONSUMER_GROUP', 'worker-group'),
        )
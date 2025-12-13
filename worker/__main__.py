# Allows `python -m worker.consumer` to run the consumer
from worker.consumer.main import main
from dotenv import load_dotenv
from pathlib import Path
from worker.utils.logger import get_logger, setup_logging

load_dotenv(dotenv_path=Path(__file__).parent.parent / '.env.local')
setup_logging()

if __name__ == '__main__':
    logger = get_logger(__name__)
    logger.info("Starting worker consumer module...")
    main()

